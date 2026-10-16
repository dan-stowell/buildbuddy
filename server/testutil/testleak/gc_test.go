package testleak

import (
	"math"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Resources that are only closed by the garbage collector.
//
// *os.File and network connections have finalizers, and *os.Process has a
// runtime.AddCleanup, that close the underlying file descriptor once the
// object is unreachable. Code that drops one of these without closing it
// leaks the descriptor until the next GC, which in a quiet production process
// can be a very long time. These tests check that such leaks are reported
// regardless of whether a GC happens to run during the check.
// ---------------------------------------------------------------------------

// leakedResource opens a resource and drops it without closing it, and
// returns the fd target prefix it is expected to show up as.
type leakedResource struct {
	name   string
	target string
	leak   func(t *testing.T)
	// cleanupOnly is set for resources released by runtime.AddCleanup
	// rather than a finalizer.
	cleanupOnly bool
}

var leakedResources = []leakedResource{
	{name: "file", target: os.DevNull, leak: leakFile},
	{name: "socket", target: "socket:", leak: leakSocket},
	{name: "pidfd", target: "anon_inode:[pidfd]", leak: leakProcess, cleanupOnly: true},
}

//go:noinline
func leakFile(t *testing.T) {
	f, err := os.Open(os.DevNull)
	require.NoError(t, err)
	_ = f
}

//go:noinline
func leakSocket(t *testing.T) {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	_ = c
}

//go:noinline
func leakProcess(t *testing.T) {
	p, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)
	_ = p
}

func skipIfNoPidfd(t *testing.T, r leakedResource) {
	if r.name != "pidfd" {
		return
	}
	before, err := readFDs()
	require.NoError(t, err)
	p, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)
	defer p.Release()
	after, err := readFDs()
	require.NoError(t, err)
	for _, e := range newFDs(before, after) {
		if e.target == r.target {
			return
		}
	}
	t.Skip("os.FindProcess doesn't use pidfds on this kernel")
}

// gcTrigger is a way for a garbage collection to happen.
type gcTrigger struct {
	name string
	gc   func()
}

var gcTriggers = []gcTrigger{
	// Some code (and many tests) call runtime.GC directly.
	{name: "explicit", gc: runtime.GC},
	// Most GCs are triggered by allocation.
	{name: "allocation", gc: allocateUntilGC},
}

var allocSink []byte

// allocateUntilGC allocates garbage until the GC has run twice, or 32MiB
// have been allocated. Keep the total well below gcHoldHeadroom: when the GC
// is held, more than that makes the GC run anyway (and runtime.AddCleanup
// cleanups with it).
func allocateUntilGC() {
	start := gcCycles()
	for n := 0; n < 32<<20 && gcCycles() < start+2; n += 64 << 10 {
		allocSink = make([]byte, 64<<10)
	}
	allocSink = nil
}

func gcCycles() uint64 {
	s := []metrics.Sample{{Name: "/gc/cycles/total:gc-cycles"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// Demonstration of the false negative, and that it is fixed: a resource is
// dropped without being closed, and a GC runs (in the test body, or while
// CheckFDs waits for the resources to be released).
func TestGC_FDOnlyClosedByGC_Caught(t *testing.T) {
	skipIfNoFDs(t)
	for _, r := range leakedResources {
		for _, trig := range gcTriggers {
			for _, when := range []string{"in test", "during settle"} {
				t.Run(r.name+"/"+trig.name+"/"+strings.ReplaceAll(when, " ", "_"), func(t *testing.T) {
					skipIfNoPidfd(t, r)
					if r.cleanupOnly && trig.name == "explicit" {
						t.Skip("a forced GC runs runtime.AddCleanup cleanups; see TestAudit_ExplicitGCRunsAddCleanups")
					}
					dogfood(t)
					f := newFakeTB(t)
					CheckFDs(f, FDSettle(failingSettle))
					r.leak(t)
					if when == "in test" {
						trig.gc()
					} else {
						// Cleanups run in reverse order, so this runs
						// before CheckFDs starts waiting.
						f.Cleanup(func() { time.AfterFunc(100*time.Millisecond, trig.gc) })
					}
					f.runCleanups()

					msg := f.requireOneError(t)
					require.Contains(t, msg, "leaked file descriptors")
					require.Contains(t, msg, r.target)
					require.Contains(t, msg, "closed only by the garbage collector")
				})
			}
		}
	}
}

func TestGC_GrowthOnlyClosedByGC_Caught(t *testing.T) {
	skipIfNoFDs(t)
	for _, r := range leakedResources {
		for _, trig := range gcTriggers {
			t.Run(r.name+"/"+trig.name, func(t *testing.T) {
				skipIfNoPidfd(t, r)
				if r.cleanupOnly && trig.name == "explicit" {
					t.Skip("a forced GC runs runtime.AddCleanup cleanups; see TestAudit_ExplicitGCRunsAddCleanups")
				}
				dogfood(t)
				f := newFakeTB(t)
				CheckGrowth(f, Growth{Iterations: 10, MaxFDs: 2, Settle: failingSettle}, func(int) {
					r.leak(t)
					trig.gc()
				})

				msg := f.requireOneError(t)
				require.Contains(t, msg, "file descriptors: +10 (1.00 per iteration")
				require.Contains(t, msg, r.target)
				require.Contains(t, msg, "closed only by the garbage collector")
			})
		}
	}
}

func TestGC_ClosedResources_Accepted(t *testing.T) {
	skipIfNoFDs(t)
	for _, trig := range gcTriggers {
		t.Run(trig.name, func(t *testing.T) {
			dogfood(t)
			f := newFakeTB(t)
			CheckFDs(f)
			var growthErrors *fakeTB
			func() {
				g := newFakeTB(t)
				growthErrors = g
				CheckGrowth(g, Growth{Iterations: 10}, func(int) {
					file, err := os.Open(os.DevNull)
					require.NoError(t, err)
					require.NoError(t, file.Close())
					conn, err := net.ListenPacket("udp", "127.0.0.1:0")
					require.NoError(t, err)
					require.NoError(t, conn.Close())
					p, err := os.FindProcess(os.Getpid())
					require.NoError(t, err)
					p.Release()
					trig.gc()
				})
			}()
			growthErrors.requireNoErrors(t)
			trig.gc()
			f.runCleanups()
			f.requireNoErrors(t)
		})
	}
}

func TestGC_AllowGC_OptsOut(t *testing.T) {
	skipIfNoFDs(t)
	// Not dogfood: an outer CheckFDs would hold the GC.
	Check(t)
	f := newFakeTB(t)
	CheckFDs(f, AllowGC(), FDSettle(failingSettle))
	leakFile(t)
	runtime.GC()
	f.runCleanups()
	// Documented: with AllowGC, a leak that the GC happened to clean up
	// is not reported.
	f.requireNoErrors(t)

	g := newFakeTB(t)
	CheckGrowth(g, Growth{Iterations: 10, AllowGC: true, Settle: failingSettle}, func(int) {
		leakFile(t)
		runtime.GC()
	})
	g.requireNoErrors(t)
}

// Documented limitation: *os.Process releases its pidfd with
// runtime.AddCleanup, and cleanups run on their own goroutines, which testleak
// can't hold. An explicit runtime.GC call during the check closes it before it
// is counted. (A GC caused by allocation can't happen while the GC is held, so
// that case is caught; see TestGC_FDOnlyClosedByGC_Caught.)
func TestAudit_ExplicitGCRunsAddCleanups(t *testing.T) {
	skipIfNoFDs(t)
	skipIfNoPidfd(t, leakedResources[2])
	dogfood(t)
	f := newFakeTB(t)
	CheckFDs(f, FDSettle(failingSettle))
	leakProcess(t)
	runtime.GC()
	f.runCleanups()
	f.requireNoErrors(t)
}

func currentGCPercent() int {
	p := debug.SetGCPercent(-1)
	debug.SetGCPercent(p)
	return p
}

func TestGC_HeldDuringCheckAndRestored(t *testing.T) {
	skipIfNoFDs(t)
	dogfood(t)
	// dogfood holds the GC too, so check the state with nested holds.
	require.Equal(t, -1, currentGCPercent())
	f := newFakeTB(t)
	CheckFDs(f)
	require.Equal(t, -1, currentGCPercent())
	limit := debug.SetMemoryLimit(-1)
	require.Less(t, limit, int64(math.MaxInt64), "a memory limit should be set as a safety net")

	// No GC runs because of allocation.
	start := gcCycles()
	for range 256 {
		allocSink = make([]byte, 64<<10)
	}
	allocSink = nil
	require.Equal(t, start, gcCycles())

	f.runCleanups()
	f.requireNoErrors(t)
	require.Equal(t, -1, currentGCPercent(), "the outer hold is still active")
	require.Equal(t, limit, debug.SetMemoryLimit(-1))
}

func TestGC_RestoredAfterCheck(t *testing.T) {
	skipIfNoFDs(t)
	// No dogfood: check the settings without an outer hold.
	gcBefore, limitBefore := currentGCPercent(), debug.SetMemoryLimit(-1)
	f := newFakeTB(t)
	CheckFDs(f)
	require.Equal(t, -1, currentGCPercent())
	f.runCleanups()
	f.requireNoErrors(t)
	require.Equal(t, gcBefore, currentGCPercent())
	require.Equal(t, limitBefore, debug.SetMemoryLimit(-1))

	g := newFakeTB(t)
	CheckGrowth(g, Growth{Iterations: 2}, func(i int) {
		if i >= 1 { // After the warmup.
			require.Equal(t, -1, currentGCPercent())
		}
	})
	g.requireNoErrors(t)
	require.Equal(t, gcBefore, currentGCPercent())
	require.Equal(t, limitBefore, debug.SetMemoryLimit(-1))

	// Finalizers run again once the GC is released.
	done := make(chan struct{})
	runtime.SetFinalizer(&gcGateObject{}, func(*gcGateObject) { close(done) })
	runtime.GC()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("finalizers are still held after the checks finished")
	}
}

// The finalizer goroutine runs gcGateFinalizer while the GC is held; it must
// not be reported as a leaked goroutine.
func TestGC_HeldFinalizerGoroutine_NotReported(t *testing.T) {
	skipIfNoFDs(t)
	f := newFakeTB(t)
	Check(f)
	fds := newFakeTB(t)
	CheckFDs(fds)
	f.runCleanups() // While the finalizer goroutine is held.
	fds.runCleanups()
	f.requireNoErrors(t)
	fds.requireNoErrors(t)
}
