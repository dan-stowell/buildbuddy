// Package testleak checks that tests don't leak goroutines or file
// descriptors.
//
// There are four checks:
//
//   - [Check] fails the test if goroutines started after the call are still
//     running at the end of the test (after a settle period).
//   - [CheckFDs] does the same for file descriptors (Linux only).
//   - [CheckGrowth] runs an operation repeatedly and fails if goroutines or
//     FDs grow by more than a bound. Use it when the long-lived owner of the
//     resources (a server, a pool, a cache) is still alive at the end of the
//     test, so an end-of-test check can't be used.
//   - [Soak] runs batches of a long, mixed workload and fails for any group of
//     goroutines or FDs that grows steadily, without being told what to look
//     for. Use it to search for leaks in long-lived services.
//
// All checks compare against a snapshot taken when they are called, so they
// cannot tell resources created by this test apart from resources created
// concurrently by other code in the same process. Don't use them in tests that
// call t.Parallel, or whose package has parallel tests that could run at the
// same time.
//
// When measuring counts yourself, avoid assert.Eventually or require.Eventually:
// their condition runs in an extra goroutine, adding one to runtime.NumGoroutine.
// CheckGrowth samples synchronously and avoids that counting pitfall.
//
// These are process-local snapshots, not ownership tracking: they cannot see
// child-process resources or work that starts after a passing snapshot. Shut
// down the owner (including scheduled callbacks) before cleanup checks run.
//
// A file descriptor that is only closed by the garbage collector (an *os.File,
// net.Conn or *os.Process dropped without Close/Release, closed later by its
// finalizer or cleanup) is a leak. CheckFDs and CheckGrowth hold the garbage
// collector while they measure, so that such leaks are reported whether or not
// a GC would have happened to run; see CheckFDs and AllowGC.
//
// Goroutine pools started before a check hide work stuck on them. In
// particular, gRPC servers built with grpc_server's options run handlers on a
// pool of GOMAXPROCS*grpc_server_worker_multiplier worker goroutines that is
// started with the server, so a stuck server handler (e.g. a proxy blocked
// sending to a client) reuses a goroutine that is already in the baseline of
// CheckGrowth, or of Check if the server was started before it, and is not
// reported. Only client-side goroutines are. When checking server handlers,
// disable the pool before starting the server:
//
//	flags.Set(t, "grpc_server_worker_multiplier", 0)
//
// Ordering matters: t.Cleanup functions run in reverse order of registration,
// so call Check and CheckFDs before setting up anything that registers a
// cleanup (servers, test environments, temp dirs), so that the leak check runs
// after those have been torn down.
package testleak

import (
	"bytes"
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// DefaultSettle is how long the checks wait for resources to be released
// before reporting a leak. Resources that are released asynchronously (e.g.
// gRPC transports, which close their reader goroutines and sockets in the
// background after ClientConn.Close returns) are not reported as long as they
// go away within this period. Passing checks return as soon as the resources
// are gone, so this only adds latency to failing tests.
const DefaultSettle = 5 * time.Second

// Check fails the test if any goroutine started after Check is called is still
// running once the test's other cleanups have finished and the settle period
// (DefaultSettle) has passed. Cleanups run in reverse order, so call Check
// before setting up anything that registers a cleanup, such as servers or test
// environments.
//
// Goroutines that already exist when Check is called are never reported, even
// if the test was supposed to stop them.
//
// opts are passed to goleak, and are typically goleak.IgnoreTopFunction or
// goleak.IgnoreAnyFunction options listing known leaks.
func Check(t testing.TB, opts ...goleak.Option) {
	t.Helper()
	checkGoroutines(t, DefaultSettle, opts...)
}

func checkGoroutines(t testing.TB, settle time.Duration, opts ...goleak.Option) {
	// The finalizer goroutine shows up as a user goroutine while CheckFDs
	// holds it.
	opts = append(slices.Clip(opts), goleak.IgnoreCurrent(), goleak.IgnoreAnyFunction(gcGateFinalizerName))
	testGoroutine := currentGoroutineID()
	t.Cleanup(func() {
		start := time.Now()
		deadline := start.Add(settle)
		for {
			// goleak.Find itself retries for ~430ms (20 retries with
			// exponential backoff capped at 100ms) before giving up, so
			// this loop doesn't need to sleep.
			err := goleak.Find(opts...)
			if err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("%s leaked goroutines still running %s after the test (and its later-registered cleanups) finished:\n%s",
					t.Name(), time.Since(start).Round(time.Millisecond), formatGoleakError(err, testGoroutine))
				return
			}
		}
	})
}

var goleakIDRegexp = regexp.MustCompile(`Goroutine (\d+) in state `)

// formatGoleakError groups the goroutines reported by goleak by stack, so that
// a leak of 100 identical goroutines produces one entry instead of 100, and
// points out the "created by" frame, which usually identifies the owner.
func formatGoleakError(err error, testGoroutine int) string {
	ids := map[int]bool{}
	for _, m := range goleakIDRegexp.FindAllStringSubmatch(err.Error(), -1) {
		id, _ := strconv.Atoi(m[1])
		ids[id] = true
	}
	var leaked []goroutine
	for _, g := range allGoroutines() {
		if ids[g.id] {
			leaked = append(leaked, g)
		}
	}
	if len(leaked) == 0 {
		// The goroutines exited between goleak's snapshot and ours.
		return err.Error()
	}
	return formatGoroutines(leaked, testGoroutine)
}

// goroutine is one goroutine parsed from a runtime.Stack dump.
type goroutine struct {
	id    int
	state string // e.g. "chan receive", without the wait duration.
	// stack is the stack trace without the header line, including the
	// "created by" line.
	stack string
	// topFunction is the function at the top of the stack.
	topFunction string
	// functions is every function in the stack, excluding the creator.
	functions []string
	// creator is the "created by" line, e.g.
	// "created by net/http.(*Server).Serve in goroutine 7".
	creator string
	// creatorGoroutine is the ID of the goroutine that started this one, or
	// 0 if unknown.
	creatorGoroutine int
}

// signature is used to group identical goroutines: it's the stack with
// argument values, PC offsets and the creator goroutine ID removed.
func (g goroutine) signature() string {
	var b strings.Builder
	b.WriteString(g.state)
	b.WriteByte('\n')
	for line := range strings.SplitSeq(g.stack, "\n") {
		if strings.HasPrefix(line, "\t") {
			if i := strings.LastIndex(line, " +0x"); i >= 0 {
				line = line[:i]
			}
		} else if strings.HasPrefix(line, "created by ") {
			if i := strings.Index(line, " in goroutine "); i >= 0 {
				line = line[:i]
			}
		} else if i := strings.LastIndex(line, "("); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

var (
	goroutineHeaderRegexp = regexp.MustCompile(`^goroutine (\d+)\b.*\[([^\]]*)\]:$`)
	creatorRegexp         = regexp.MustCompile(` in goroutine (\d+)$`)
)

// allGoroutines returns every goroutine in the process except the calling one.
func allGoroutines() []goroutine {
	all := parseStacks(stackDump(true))
	self := currentGoroutineID()
	return slices.DeleteFunc(all, func(g goroutine) bool { return g.id == self })
}

func stackDump(all bool) []byte {
	buf := make([]byte, 64<<10)
	for {
		n := runtime.Stack(buf, all)
		if n < len(buf) {
			return buf[:n]
		}
		buf = make([]byte, 2*len(buf))
	}
}

func currentGoroutineID() int {
	gs := parseStacks(stackDump(false))
	if len(gs) == 0 {
		return 0
	}
	return gs[0].id
}

func parseStacks(dump []byte) []goroutine {
	var out []goroutine
	for block := range bytes.SplitSeq(bytes.TrimSpace(dump), []byte("\n\n")) {
		header, rest, _ := strings.Cut(string(block), "\n")
		m := goroutineHeaderRegexp.FindStringSubmatch(header)
		if m == nil {
			continue
		}
		g := goroutine{stack: rest}
		g.id, _ = strconv.Atoi(m[1])
		g.state, _, _ = strings.Cut(m[2], ",")
		for line := range strings.SplitSeq(rest, "\n") {
			if line == "" || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "...") {
				continue
			}
			if strings.HasPrefix(line, "created by ") {
				g.creator = line
				if cm := creatorRegexp.FindStringSubmatch(line); cm != nil {
					g.creatorGoroutine, _ = strconv.Atoi(cm[1])
				}
				continue
			}
			fn := line
			if i := strings.LastIndex(fn, "("); i > 0 {
				fn = fn[:i]
			}
			if g.topFunction == "" {
				g.topFunction = fn
			}
			g.functions = append(g.functions, fn)
		}
		out = append(out, g)
	}
	return out
}

// isTestingGoroutine reports whether g is a goroutine run by the testing
// package itself (the main test runner, or tests waiting for their parallel
// siblings or subtests), mirroring goleak's default filter.
func isTestingGoroutine(g goroutine) bool {
	switch g.topFunction {
	case "testing.RunTests", "testing.(*T).Run", "testing.(*T).Parallel", "testing.runFuzzing", "testing.runFuzzTests":
		return strings.HasPrefix(g.state, "chan receive")
	}
	if slices.Contains(g.functions, gcGateFinalizerName) {
		// The finalizer goroutine, held by CheckFDs or CheckGrowth.
		return true
	}
	return g.topFunction == "os/signal.signal_recv" || g.topFunction == "os/signal.loop" || slices.Contains(g.functions, "runtime.ensureSigM")
}

const maxGroupsReported = 20

// formatGoroutines describes gs grouped by identical stacks, largest groups
// first.
func formatGoroutines(gs []goroutine, testGoroutine int) string {
	type group struct {
		gs []goroutine
	}
	groups := map[string]*group{}
	var order []string
	for _, g := range gs {
		sig := g.signature()
		if groups[sig] == nil {
			groups[sig] = &group{}
			order = append(order, sig)
		}
		groups[sig].gs = append(groups[sig].gs, g)
	}
	slices.SortStableFunc(order, func(a, b string) int { return len(groups[b].gs) - len(groups[a].gs) })

	var b strings.Builder
	fmt.Fprintf(&b, "%d goroutine(s) in %d distinct stack(s):\n", len(gs), len(order))
	for i, sig := range order {
		if i == maxGroupsReported {
			fmt.Fprintf(&b, "\n... and %d more distinct stack(s)\n", len(order)-i)
			break
		}
		grp := groups[sig].gs
		g := grp[0]
		var ids []string
		for j, x := range grp {
			if j == 10 {
				ids = append(ids, "...")
				break
			}
			ids = append(ids, strconv.Itoa(x.id))
		}
		creator := g.creator
		if creator == "" {
			creator = "(no creator frame)"
		}
		fmt.Fprintf(&b, "\n=== %d x [%s] top: %s\n    %s", len(grp), g.state, g.topFunction, creator)
		if testGoroutine != 0 && g.creatorGoroutine == testGoroutine {
			b.WriteString(" (the goroutine that called the check)")
		}
		fmt.Fprintf(&b, "\n    goroutine ids: %s\n", strings.Join(ids, ", "))
		for line := range strings.SplitSeq(strings.TrimRight(g.stack, "\n"), "\n") {
			b.WriteString("    ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
