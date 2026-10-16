//go:build (darwin || linux) && !android && !ios

package commandutil

import (
	"os/exec"
	"testing"
	"time"
)

func TestKillProcessTree_ReapedRootDoesNotSignalReusedPID(t *testing.T) {
	root := exec.Command("sh", "-c", "exit 0")
	if err := root.Run(); err != nil {
		t.Fatal(err)
	}
	unrelated := exec.Command("sleep", "30")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- unrelated.Wait() }()
	defer func() {
		unrelated.Process.Kill()
	}()
	// Simulate reuse of the numeric PID without relying on kernel PID churn.
	// The root's os.Process handle still records that it has been reaped.
	root.Process.Pid = unrelated.Process.Pid
	p := &process{cmd: root}
	if err := p.killProcessTree(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("cleanup killed a reused root PID: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unrelated.Process.Kill()
	<-done
}
