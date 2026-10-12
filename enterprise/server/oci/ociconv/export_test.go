package ociconv

import (
	"sync"
	"testing"
)

// ObserveConversionLockForTest replaces the lock implementation with per-key
// mutexes that report failed TryLock attempts. A signal proves the worker has
// reached a contended acquisition, rather than only its caller's singleflight
// wait. Tests keep the holder blocked until consuming the signal.
// Call only from nonparallel tests, before starting conversion workers.
func ObserveConversionLockForTest(t testing.TB) <-chan struct{} {
	t.Helper()
	waiting := make(chan struct{}, 2)
	var mutexes sync.Map
	previous := lockConversion
	lockConversion = func(key string) func() {
		value, _ := mutexes.LoadOrStore(key, &sync.Mutex{})
		mu := value.(*sync.Mutex)
		if !mu.TryLock() {
			waiting <- struct{}{}
			mu.Lock()
		}
		return mu.Unlock
	}
	t.Cleanup(func() { lockConversion = previous })
	return waiting
}
