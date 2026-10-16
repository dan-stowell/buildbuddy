package testleak

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// FDOption configures CheckFDs.
type FDOption func(*fdConfig)

type fdConfig struct {
	settle  time.Duration
	ignore  []*regexp.Regexp
	allowGC bool
}

// IgnoreFDs makes CheckFDs ignore new file descriptors whose description
// matches the regular expression. The description is what appears in the
// failure message, e.g. "/tmp/foo.db", "pipe:[1234]", "anon_inode:[eventfd]",
// or "socket:[5678] tcp 127.0.0.1:41234 -> 127.0.0.1:8080 ESTABLISHED".
// Use this only for resources that are intentionally process-wide and
// created lazily during the test, and say why next to the call.
func IgnoreFDs(pattern string) FDOption {
	re := regexp.MustCompile(pattern)
	return func(c *fdConfig) { c.ignore = append(c.ignore, re) }
}

// FDSettle overrides how long CheckFDs waits for file descriptors to be
// closed before reporting a leak. The default is DefaultSettle.
func FDSettle(d time.Duration) FDOption {
	return func(c *fdConfig) { c.settle = d }
}

// AllowGC lets the garbage collector run normally while CheckFDs is in
// progress. By default, CheckFDs holds the garbage collector (see below), so
// that file descriptors that are dropped without being closed are reported
// even though a finalizer would eventually close them. With AllowGC, such
// leaks are only reported if no garbage collection happens to run before the
// check. Use it only for tests that allocate so much that holding the GC is a
// problem, or whose code under test depends on finalizers running, and say
// why next to the call. It has no effect while another check (e.g. an
// enclosing CheckFDs) is holding the GC.
func AllowGC() FDOption {
	return func(c *fdConfig) { c.allowGC = true }
}

// CheckFDs fails the test if any file descriptor opened after CheckFDs is
// called is still open once the test's other cleanups have finished and the
// settle period has passed. Like Check, call it before setting up anything that
// registers a cleanup. It does nothing on platforms other than Linux, or if
// /proc/self/fd can't be read.
//
// A file descriptor is "new" if its number wasn't open at the start, or was
// open with a different target (e.g. it was closed and reused).
//
// Reopening the same target at the same descriptor number is indistinguishable
// from retaining the original descriptor. This also applies to anonymous inode
// targets with identical descriptions. Use CheckGrowth to detect accumulation,
// and assert owner-specific closure when replacement matters.
//
// The Go runtime lazily opens an epoll instance and an eventfd the first time
// anything uses the network poller, and never closes them. On Linux that
// happens on the first socket, pipe, or even regular file opened with the os
// package, so in practice it has usually happened before a test starts, but
// not always. CheckFDs makes sure the poller is initialized before taking its
// snapshot, so these are never reported.
//
// *os.File, network connections and *os.Process (a pidfd) close their file
// descriptor in a finalizer or cleanup when they are garbage collected. A
// descriptor that is only closed that way is reported as a leak: from the
// call to CheckFDs until the end of the check, the garbage collector is turned
// off (with a memory limit of 1 GiB more than in use at the start, as a safety
// net) and finalizers are held, so the result doesn't depend on GC timing.
// The report says which leaked descriptors a forced GC closed afterwards. Use
// AllowGC to opt out.
func CheckFDs(t testing.TB, opts ...FDOption) {
	t.Helper()
	cfg := fdConfig{settle: DefaultSettle}
	for _, o := range opts {
		o(&cfg)
	}
	if !fdsSupported() {
		return
	}
	var hold *gcHold
	if !cfg.allowGC {
		hold = holdGC()
	}
	releaseGC := func() {
		if hold == nil {
			return
		}
		if note := gcNote(hold.release()); note != "" {
			t.Logf("%s", note)
		}
	}
	warmNetpoller()
	before, err := readFDs()
	if err != nil {
		releaseGC()
		t.Logf("testleak: not checking file descriptors: %s", err)
		return
	}
	t.Cleanup(func() {
		defer releaseGC()
		start := time.Now()
		var leaked []fdEntry
		for {
			after, err := readFDs()
			if err != nil {
				t.Errorf("testleak: read file descriptors: %s", err)
				return
			}
			leaked = cfg.filter(newFDs(before, after))
			if len(leaked) == 0 {
				return
			}
			if time.Since(start) > cfg.settle {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		elapsed := time.Since(start)
		closedByGC := fdsClosedByGC(leaked)
		t.Errorf("%s leaked file descriptors still open %s after the test (and its later-registered cleanups) finished:\n%s%s",
			t.Name(), elapsed.Round(time.Millisecond), gcClosedNote(closedByGC), describeFDs(leaked, closedByGC))
	})
}

func fdsSupported() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := os.Stat("/proc/self/fd")
	return err == nil
}

// warmNetpoller makes sure the Go runtime's network poller (an epoll fd plus
// an eventfd used to wake it up) has been initialized, so that the FDs it
// opens on first use are part of the baseline snapshot. Opening a pipe through
// the os package registers it with the poller, which initializes it.
func warmNetpoller() {
	r, w, err := os.Pipe()
	if err != nil {
		return
	}
	r.Close()
	w.Close()
}

type fdEntry struct {
	fd     int
	target string
	// desc is the target, with sockets resolved to addresses if possible.
	desc string
}

// readFDs returns the process's open file descriptors and their targets,
// excluding the descriptor used to list the directory.
func readFDs() (map[int]string, error) {
	dir, err := os.Open("/proc/self/fd")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	self := int(dir.Fd())
	fds := make(map[int]string, len(names))
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil || fd == self {
			continue
		}
		target, err := os.Readlink("/proc/self/fd/" + name)
		if err != nil {
			// Closed after we listed the directory.
			continue
		}
		fds[fd] = target
	}
	return fds, nil
}

func newFDs(before, after map[int]string) []fdEntry {
	var out []fdEntry
	for fd, target := range after {
		if prev, ok := before[fd]; ok && prev == target {
			continue
		}
		out = append(out, fdEntry{fd: fd, target: target, desc: target})
	}
	slices.SortFunc(out, func(a, b fdEntry) int { return a.fd - b.fd })
	return out
}

// filter resolves socket descriptions and removes ignored entries.
func (c *fdConfig) filter(fds []fdEntry) []fdEntry {
	if len(fds) == 0 {
		return nil
	}
	resolveFDs(fds)
	return slices.DeleteFunc(fds, func(e fdEntry) bool {
		for _, re := range c.ignore {
			if re.MatchString(e.desc) {
				return true
			}
		}
		return false
	})
}

const maxFDsReported = 100

// describeFDs describes fds, marking the ones in closedByGC.
func describeFDs(fds []fdEntry, closedByGC map[int]bool) string {
	counts := map[string]int{}
	for _, e := range fds {
		counts[fdKind(e.target)]++
	}
	var kinds []string
	for k, n := range counts {
		kinds = append(kinds, fmt.Sprintf("%d %s", n, k))
	}
	slices.Sort(kinds)
	var b strings.Builder
	fmt.Fprintf(&b, "%d file descriptor(s): %s\n", len(fds), strings.Join(kinds, ", "))
	for i, e := range fds {
		if i == maxFDsReported {
			fmt.Fprintf(&b, "  ... and %d more\n", len(fds)-i)
			break
		}
		fmt.Fprintf(&b, "  fd %d -> %s", e.fd, e.desc)
		if closedByGC[e.fd] {
			b.WriteString(" (closed by GC)")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func fdKind(target string) string {
	switch {
	case strings.HasPrefix(target, "socket:"):
		return "socket"
	case strings.HasPrefix(target, "pipe:"):
		return "pipe"
	case strings.HasPrefix(target, "anon_inode:"):
		return strings.TrimPrefix(target, "anon_inode:")
	case strings.HasPrefix(target, "/"):
		return "file"
	}
	return "other"
}

var inodeRegexp = regexp.MustCompile(`^(?:socket|pipe):\[(\d+)\]$`)

// resolveFDs fills in desc for sockets (with their addresses, from
// /proc/self/net) and pipes (with the other in-process end, if any).
func resolveFDs(fds []fdEntry) {
	var sockets map[string]string
	var all map[int]string
	for i := range fds {
		e := &fds[i]
		m := inodeRegexp.FindStringSubmatch(e.target)
		if m == nil {
			continue
		}
		inode := m[1]
		if strings.HasPrefix(e.target, "socket:") {
			if sockets == nil {
				sockets = readSocketTable()
			}
			if s, ok := sockets[inode]; ok {
				e.desc = e.target + " " + s
			}
			continue
		}
		// A pipe: show other descriptors for the same pipe.
		if all == nil {
			all, _ = readFDs()
		}
		var peers []string
		for fd, target := range all {
			if target == e.target && fd != e.fd {
				peers = append(peers, strconv.Itoa(fd))
			}
		}
		if len(peers) > 0 {
			slices.Sort(peers)
			e.desc = fmt.Sprintf("%s (also open as fd %s)", e.target, strings.Join(peers, ", "))
		}
	}
}

var tcpStates = map[string]string{
	"01": "ESTABLISHED", "02": "SYN_SENT", "03": "SYN_RECV", "04": "FIN_WAIT1",
	"05": "FIN_WAIT2", "06": "TIME_WAIT", "07": "CLOSE", "08": "CLOSE_WAIT",
	"09": "LAST_ACK", "0A": "LISTEN", "0B": "CLOSING",
}

// readSocketTable maps socket inode numbers to a description, read from the
// network namespace of this process.
func readSocketTable() map[string]string {
	out := map[string]string{}
	for _, proto := range []string{"tcp", "tcp6", "udp", "udp6"} {
		readInetTable(proto, out)
	}
	readUnixTable(out)
	return out
}

func readInetTable(proto string, out map[string]string) {
	f, err := os.Open("/proc/self/net/" + proto)
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Scan() // header
	for s.Scan() {
		// sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode ...
		fields := strings.Fields(s.Text())
		if len(fields) < 10 {
			continue
		}
		state := fields[3]
		if strings.HasPrefix(proto, "tcp") {
			if name, ok := tcpStates[state]; ok {
				state = name
			}
		} else {
			state = ""
		}
		desc := fmt.Sprintf("%s %s -> %s", proto, parseProcAddr(fields[1]), parseProcAddr(fields[2]))
		if state != "" {
			desc += " " + state
		}
		out[fields[9]] = desc
	}
}

// parseProcAddr parses an address like "0100007F:1F90" from /proc/net/tcp.
// The IP is a sequence of 32-bit words, each in host (little-endian) byte
// order.
func parseProcAddr(s string) string {
	ipHex, portHex, ok := strings.Cut(s, ":")
	if !ok {
		return s
	}
	raw, err := hex.DecodeString(ipHex)
	if err != nil || len(raw)%4 != 0 {
		return s
	}
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return s
	}
	return net.JoinHostPort(net.IP(raw).String(), strconv.FormatUint(port, 10))
}

func readUnixTable(out map[string]string) {
	f, err := os.Open("/proc/self/net/unix")
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Scan() // header
	for s.Scan() {
		// Num RefCount Protocol Flags Type St Inode [Path]
		fields := strings.Fields(s.Text())
		if len(fields) < 7 {
			continue
		}
		desc := "unix"
		if len(fields) >= 8 {
			desc += " " + fields[7]
		} else {
			desc += " (unnamed)"
		}
		out[fields[6]] = desc
	}
}
