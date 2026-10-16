//go:build unix

package testport

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockFiles keeps the lock files open: if they became unreachable, their
// finalizers would close them, releasing the locks, at the next GC. Guarded
// by the port leaser's mutex.
var lockFiles []*os.File

// lockPort acquires an exclusive cross-process lock for the given port to
// prevent parallel test processes from choosing the same port. The lock is
// released automatically when the process exits.
func lockPort(port int) bool {
	lockPath := filepath.Join(os.TempDir(), fmt.Sprintf("testport.%d.lock", port))
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	// Intentionally not closing f: the flock is held for the lifetime of the
	// process, preventing other test processes from claiming this port.
	lockFiles = append(lockFiles, f)
	return true
}
