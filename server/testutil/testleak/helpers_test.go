package testleak

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeTB records errors instead of failing, so we can test that the checks
// fail when they should. Methods that the checks don't use panic.
type fakeTB struct {
	testing.TB
	name string

	mu       sync.Mutex
	errors   []string
	cleanups []func()
}

func newFakeTB(t *testing.T) *fakeTB {
	f := &fakeTB{name: t.Name() + "/fake"}
	// If the test fails before running the fake's cleanups, run them anyway.
	t.Cleanup(f.runCleanups)
	return f
}

func (f *fakeTB) Helper()      {}
func (f *fakeTB) Name() string { return f.name }
func (f *fakeTB) Cleanup(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanups = append(f.cleanups, fn)
}
func (f *fakeTB) Errorf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}
func (f *fakeTB) Logf(format string, args ...any) {}

// runCleanups runs the registered cleanups in reverse order, like testing.T.
func (f *fakeTB) runCleanups() {
	for {
		f.mu.Lock()
		if len(f.cleanups) == 0 {
			f.mu.Unlock()
			return
		}
		fn := f.cleanups[len(f.cleanups)-1]
		f.cleanups = f.cleanups[:len(f.cleanups)-1]
		f.mu.Unlock()
		fn()
	}
}

func (f *fakeTB) allErrors() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.errors, "\n")
}

func (f *fakeTB) requireNoErrors(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Empty(t, f.errors, "the check reported a leak, but there is none")
}

// requireOneError checks that the fake failed exactly once, and returns the
// message.
func (f *fakeTB) requireOneError(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.errors, 1, "expected the check to report exactly one failure")
	t.Logf("leak report (expected):\n%s", f.errors[0])
	return f.errors[0]
}

// Settle period used for checks that are expected to fail, to keep the test
// fast. Checks that are expected to pass use the real default.
const failingSettle = time.Second

// leakyWorker blocks until stop is closed.
func leakyWorker(stop <-chan struct{}) {
	<-stop
}

// delayedWorker exits delay after stop is closed, like a goroutine that
// notices a closed connection asynchronously.
func delayedWorker(stop <-chan struct{}, delay time.Duration) {
	<-stop
	time.Sleep(delay)
}

// dogfood checks that the test itself doesn't leak, which also makes sure the
// synthetic leaks are cleaned up and don't affect other tests.
func dogfood(t *testing.T) {
	Check(t)
	CheckFDs(t)
}

func skipIfNoFDs(t *testing.T) {
	if !fdsSupported() {
		t.Skip("file descriptor checks are only supported on Linux")
	}
}
