package testleak

import (
	"fmt"
	"reflect"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"sync"
	"time"
)

// Holding the garbage collector.
//
// *os.File and net connections have finalizers, and *os.Process (a pidfd on
// Linux) has a runtime.AddCleanup, that close the file descriptor once the
// object becomes unreachable. A file descriptor that is dropped without being
// closed is a leak: in production it stays open until the next GC, which can
// be arbitrarily far away. But if a GC happens to run during a check, the
// finalizer closes it before it is counted, and the check passes. Whether
// that happens depends on allocation and timing, so the result is not
// deterministic.
//
// While CheckFDs or CheckGrowth (when counting FDs) is in progress, testleak
// therefore:
//
//   - turns off the garbage collector (debug.SetGCPercent(-1)), so that no GC
//     runs because of allocation, and no finalizer or cleanup is queued;
//   - sets a memory limit of the memory in use at the start plus
//     gcHoldHeadroom, so a test that allocates a lot still collects garbage
//     instead of running out of memory;
//   - blocks the finalizer goroutine, so that if a GC does run (an explicit
//     runtime.GC call, or the memory limit) finalizers are queued but don't
//     run until the check is over. Cleanups registered with
//     runtime.AddCleanup run on other goroutines and can't be held this way.
//
// When a leak is found, testleak forces a GC and reports which of the leaked
// descriptors were closed by it.

// gcHoldHeadroom is how much memory a test may allocate while the garbage
// collector is held before the GC runs anyway.
const gcHoldHeadroom = 1 << 30

// gcHolds is the process-wide state of the GC hold, shared by nested checks.
var gcHolds struct {
	mu        sync.Mutex
	n         int
	oldGC     int
	oldLimit  int64
	gate      chan struct{} // closed to release the finalizer goroutine
	ownCycles uint64        // GC cycles started by testleak itself
}

// gcHold is one check's hold on the garbage collector.
type gcHold struct {
	startCycles uint64
	startOwn    uint64
	gated       bool
	released    bool
}

// holdGC turns off the garbage collector and blocks finalizers until release
// is called.
func holdGC() *gcHold {
	gcHolds.mu.Lock()
	defer gcHolds.mu.Unlock()
	if gcHolds.n == 0 {
		gcHolds.oldGC = debug.SetGCPercent(-1)
		gcHolds.oldLimit = debug.SetMemoryLimit(-1)
		debug.SetMemoryLimit(min(gcHolds.oldLimit, int64(goMemoryInUse()+gcHoldHeadroom)))
	}
	gcHolds.n++
	if gcHolds.gate == nil {
		gcHolds.gate = armFinalizerGate()
	}
	return &gcHold{
		startCycles: gcCyclesTotal(),
		startOwn:    gcHolds.ownCycles,
		gated:       gcHolds.gate != nil,
	}
}

// release undoes holdGC (once all nested holds are released), and returns
// how many GC cycles ran during the hold, other than ones started by testleak.
func (h *gcHold) release() (otherCycles uint64, gated bool) {
	gcHolds.mu.Lock()
	defer gcHolds.mu.Unlock()
	otherCycles = gcCyclesTotal() - h.startCycles - (gcHolds.ownCycles - h.startOwn)
	if h.released {
		return otherCycles, h.gated
	}
	h.released = true
	gcHolds.n--
	if gcHolds.n == 0 {
		if gcHolds.gate != nil {
			close(gcHolds.gate)
			gcHolds.gate = nil
		}
		debug.SetGCPercent(gcHolds.oldGC)
		debug.SetMemoryLimit(gcHolds.oldLimit)
	}
	return otherCycles, h.gated
}

// gcNote describes GC cycles that ran during a hold, for logging.
func gcNote(otherCycles uint64, gated bool) string {
	if otherCycles == 0 && gated {
		return ""
	}
	note := fmt.Sprintf("testleak: the garbage collector ran %d time(s) during the check (explicit runtime.GC calls, or more than %d MiB allocated)", otherCycles, gcHoldHeadroom>>20)
	if gated {
		return note + "; file descriptors closed only by runtime.AddCleanup cleanups (e.g. *os.Process pidfds) may not have been reported"
	}
	return note + "; the finalizer goroutine could not be held, so file descriptors closed only by the garbage collector may not have been reported"
}

// gcGateObject is garbage collected to block the finalizer goroutine.
type gcGateObject struct {
	entered chan struct{}
	release chan struct{}
}

// gcGateFinalizer blocks the finalizer goroutine until the gate is released.
// Finalizers run one at a time on a single goroutine, so no other finalizer
// runs in the meantime.
func gcGateFinalizer(o *gcGateObject) {
	close(o.entered)
	<-o.release
}

var gcGateFinalizerName = runtime.FuncForPC(reflect.ValueOf(gcGateFinalizer).Pointer()).Name()

// armFinalizerGate blocks the finalizer goroutine, and returns the channel to
// close to release it, or nil if that didn't work (e.g. another finalizer is
// taking a long time). Finalizers already queued run first. Must be called
// with gcHolds.mu held.
//
//go:noinline
func armFinalizerGate() chan struct{} {
	entered, release := make(chan struct{}), make(chan struct{})
	runtime.SetFinalizer(&gcGateObject{entered: entered, release: release}, gcGateFinalizer)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		forceGC()
		select {
		case <-entered:
			return release
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Release it if it ever runs.
	close(release)
	return nil
}

// forceGC runs a GC cycle. Must be called with gcHolds.mu held.
func forceGC() {
	gcHolds.ownCycles++
	runtime.GC()
}

// fdsClosedByGC releases held finalizers and forces garbage collections until
// all of leaked are closed, or a deadline passes, and returns the descriptors
// that were closed. If other checks are still holding the GC, the hold is
// re-established afterwards.
func fdsClosedByGC(leaked []fdEntry) map[int]bool {
	gcHolds.mu.Lock()
	defer gcHolds.mu.Unlock()
	if gcHolds.gate != nil {
		close(gcHolds.gate)
		gcHolds.gate = nil
	}
	closed := map[int]bool{}
	deadline := time.Now().Add(time.Second)
	for len(closed) < len(leaked) && time.Now().Before(deadline) {
		forceGC()
		time.Sleep(20 * time.Millisecond)
		now, err := readFDs()
		if err != nil {
			break
		}
		for _, e := range leaked {
			if now[e.fd] != e.target {
				closed[e.fd] = true
			}
		}
	}
	if gcHolds.n > 0 {
		gcHolds.gate = armFinalizerGate()
	}
	return closed
}

// gcClosedNote describes the leaked FDs closed by fdsClosedByGC.
func gcClosedNote(closed map[int]bool) string {
	if len(closed) == 0 {
		return ""
	}
	return fmt.Sprintf("%d of these were closed only by the garbage collector (a finalizer or runtime.AddCleanup) when testleak forced a GC afterwards, "+
		"so they were dropped without being closed; in production they stay open until the next GC. Close them explicitly. "+
		"(They are marked \"closed by GC\" below.)\n", len(closed))
}

func gcCyclesTotal() uint64 {
	s := []metrics.Sample{{Name: "/gc/cycles/total:gc-cycles"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// goMemoryInUse is the memory mapped by the Go runtime, which is what the
// memory limit applies to.
func goMemoryInUse() uint64 {
	s := []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
	}
	metrics.Read(s)
	return s[0].Value.Uint64() - s[1].Value.Uint64()
}
