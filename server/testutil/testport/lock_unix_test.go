//go:build unix

package testport

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// The lock on a leased port must be held for the life of the process, even
// after garbage collections, so that other test processes don't lease it.
func TestLeasedPortStaysLockedAfterGC(t *testing.T) {
	port := FindFree(t)
	lockPath := filepath.Join(os.TempDir(), fmt.Sprintf("testport.%d.lock", port))
	// flock locks belong to the open file description, so a second open in
	// this process conflicts with the lease's lock like another process.
	f, err := os.Open(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Finalizers run asynchronously after a GC; give them a moment.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			t.Fatalf("port %d is no longer locked after a GC; another test process could lease it", port)
		}
	}
}
