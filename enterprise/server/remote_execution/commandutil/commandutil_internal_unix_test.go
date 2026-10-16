//go:build (darwin || linux) && !android && !ios

package commandutil

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// Model numeric PGID reuse after the root and all original group members have
// exited. A setsid-escaped pipe holder can keep output draining in this state.
// This uses the same reaped-handle substitution as the root PID safety test.
func TestKillProcessTree_ReviewReusedGroup(t *testing.T) {
	root := exec.Command("sh", "-c", "exit 0")
	root.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := root.Run(); err != nil {
		t.Fatal(err)
	}
	unrelated := exec.Command("sleep", "30")
	unrelated.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- unrelated.Wait() }()
	defer unrelated.Process.Kill()
	root.Process.Pid = unrelated.Process.Pid
	p := &process{cmd: root}
	if err := p.killProcessTree(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("cleanup killed unrelated reused process group: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unrelated.Process.Kill()
	<-done
}
