//go:build linux

package commandutil_test

// V3 independent reproducers (measurement-only; not intended for merge).

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/commandutil"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func v3PipeFDs(t testing.TB) int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		tgt, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err == nil && strings.HasPrefix(tgt, "pipe:") {
			n++
		}
	}
	return n
}

func v3CountGoroutines(substr string) int {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, substr) {
			n++
		}
	}
	return n
}

func v3ReadPid(t testing.TB, path string) int {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && strings.HasSuffix(string(b), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pid file %s not written", path)
	return 0
}

func v3Alive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// state is the field after ")"
	s := string(b)
	i := strings.LastIndex(s, ")")
	return i >= 0 && i+2 < len(s) && s[i+2] != 'Z'
}

func v3Gone(pid int) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return os.IsNotExist(err)
}

// Claim A: background child holds stdout/stderr pipes, root already reaped,
// then ctx is cancelled.
func TestV3_ClaimA(t *testing.T) {
	for _, n := range []int{1, 10, 50} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			dir := t.TempDir()
			baseFDs := v3PipeFDs(t)
			baseG := runtime.NumGoroutine()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make([]chan struct{}, n)
			childPids := make([]int, n)
			for i := range n {
				done[i] = make(chan struct{})
				rootFile := filepath.Join(dir, fmt.Sprintf("root%d", i))
				childFile := filepath.Join(dir, fmt.Sprintf("child%d", i))
				cmd := &repb.Command{Arguments: []string{"sh", "-c",
					fmt.Sprintf(`echo $$ > %s; sleep 100 & echo $! > %s; echo hi`, rootFile, childFile)}}
				go func(i int) {
					defer close(done[i])
					commandutil.RunWithOpts(ctx, cmd, &commandutil.RunOpts{Stdio: &interfaces.Stdio{Stdout: &syncBuf{}, Stderr: &syncBuf{}}})
				}(i)
				rootPid := v3ReadPid(t, rootFile)
				childPids[i] = v3ReadPid(t, childFile)
				// wait until root is reaped
				for !v3Gone(rootPid) {
					time.Sleep(time.Millisecond)
				}
			}
			t.Cleanup(func() {
				for _, p := range childPids {
					syscall.Kill(p, syscall.SIGKILL)
				}
				for _, d := range done {
					<-d
				}
			})
			preCancelFDs := v3PipeFDs(t) - baseFDs
			cancelAt := time.Now()
			cancel()
			returned := 0
			var maxLat time.Duration
			deadline := time.After(3 * time.Second)
		loop:
			for i := range n {
				select {
				case <-done[i]:
					returned++
					maxLat = time.Since(cancelAt)
				case <-deadline:
					break loop
				}
			}
			for i := returned; i < n; i++ {
				select {
				case <-done[i]:
					returned++
				default:
				}
			}
			alive := 0
			deadline2 := time.Now().Add(2 * time.Second)
			for {
				alive = 0
				for _, p := range childPids {
					if v3Alive(p) {
						alive++
					}
				}
				if alive == 0 || time.Now().After(deadline2) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			fds := v3PipeFDs(t) - baseFDs
			g := runtime.NumGoroutine() - baseG
			awaitG := v3CountGoroutines("os/exec.(*Cmd).writerDescriptor")
			t.Logf("V3A N=%d returned=%d/%d maxLatency=%v pipeFDsBeforeCancel=%d pipeFDsAfter=%d goroutinesDelta=%d copyGoroutines=%d childrenAlive=%d",
				n, returned, n, maxLat, preCancelFDs, fds, g, awaitG, alive)
			if returned != n || alive != 0 {
				t.Errorf("leak: %d/%d Run calls still blocked 3s after cancel; %d children alive; pipe FDs +%d", n-returned, n, alive, fds)
			}
		})
	}
}

func v3PPid(pid int) int {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return -1
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndex(s, ")")+2:])
	pp, _ := strconv.Atoi(f[1])
	return pp
}

// Claim A, realistic variants: cancel arrives while the root is still alive.
func TestV3_ClaimA_CancelWhileRootAlive(t *testing.T) {
	for _, tc := range []struct{ name, script string }{
		{"direct-child", `echo $$ > %[1]s; sleep 100 & echo $! > %[2]s; sleep 100`},
		{"double-fork", `echo $$ > %[1]s; (sleep 100 & echo $! > %[2]s); sleep 100`},
		{"setsid-double-fork", `echo $$ > %[1]s; (setsid sleep 100 & echo $! > %[2]s); sleep 100`},
	} {
		for _, n := range []int{1, 10, 50} {
			t.Run(fmt.Sprintf("%s/N=%d", tc.name, n), func(t *testing.T) {
				dir := t.TempDir()
				baseFDs := v3PipeFDs(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make([]chan struct{}, n)
				childPids := make([]int, n)
				for i := range n {
					done[i] = make(chan struct{})
					rootFile := filepath.Join(dir, fmt.Sprintf("root%d", i))
					childFile := filepath.Join(dir, fmt.Sprintf("child%d", i))
					cmd := &repb.Command{Arguments: []string{"sh", "-c", fmt.Sprintf(tc.script, rootFile, childFile)}}
					go func(i int) {
						defer close(done[i])
						commandutil.RunWithOpts(ctx, cmd, &commandutil.RunOpts{Stdio: &interfaces.Stdio{Stdout: &syncBuf{}, Stderr: &syncBuf{}}})
					}(i)
					rootPid := v3ReadPid(t, rootFile)
					childPids[i] = v3ReadPid(t, childFile)
					if tc.name != "direct-child" {
						for v3PPid(childPids[i]) == rootPid || v3PPid(childPids[i]) == -1 && !v3Gone(childPids[i]) {
							time.Sleep(time.Millisecond)
						}
					}
				}
				t.Cleanup(func() {
					for _, p := range childPids {
						syscall.Kill(p, syscall.SIGKILL)
					}
					for _, d := range done {
						<-d
					}
				})
				cancelAt := time.Now()
				cancel()
				returned := 0
				var lat time.Duration
				deadline := time.After(3 * time.Second)
			loop:
				for i := range n {
					select {
					case <-done[i]:
						returned++
						lat = time.Since(cancelAt)
					case <-deadline:
						break loop
					}
				}
				alive := 0
				for _, p := range childPids {
					if v3Alive(p) {
						alive++
					}
				}
				t.Logf("V3A-alive %s N=%d returned=%d/%d maxLatency=%v pipeFDsAfter=%d childrenAlive=%d", tc.name, n, returned, n, lat, v3PipeFDs(t)-baseFDs, alive)
				if returned != n || alive != 0 {
					t.Errorf("%d Run calls blocked; %d children alive", n-returned, alive)
				}
			})
		}
	}
}

// Claim B: signal forwarder goroutine outlives the command while ctx stays open.
func TestV3_ClaimB(t *testing.T) {
	for _, n := range []int{1, 10, 50} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			base := v3CountGoroutines("commandutil.forwardSignals")
			baseG := runtime.NumGoroutine()
			sig := make(chan syscall.Signal, 1)
			for range n {
				res := commandutil.RunWithOpts(ctx, &repb.Command{Arguments: []string{"true"}}, &commandutil.RunOpts{Signal: sig})
				if res.Error != nil {
					t.Fatal(res.Error)
				}
			}
			var fwd int
			deadline := time.Now().Add(2 * time.Second)
			for {
				fwd = v3CountGoroutines("commandutil.forwardSignals") - base
				if fwd == 0 || time.Now().After(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			g := runtime.NumGoroutine() - baseG
			cancel()
			after := 0
			deadline = time.Now().Add(2 * time.Second)
			for {
				after = v3CountGoroutines("commandutil.forwardSignals") - base
				if after == 0 || time.Now().After(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Logf("V3B N=%d forwardersAliveWhileCtxOpen=%d goroutinesDelta=%d forwardersAfterCancel=%d", n, fwd, g, after)
			if fwd != 0 {
				t.Errorf("leak: %d forwardSignals goroutines alive after %d completed commands", fwd, n)
			}
		})
	}
}
