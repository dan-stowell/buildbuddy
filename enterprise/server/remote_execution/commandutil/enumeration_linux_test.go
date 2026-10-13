//go:build linux

package commandutil_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/commandutil"
	"github.com/stretchr/testify/require"
)

func TestProcessEnumeration(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission error coverage requires an unprivileged user")
	}
	root := t.TempDir()
	t.Setenv("HOST_PROC", root)
	// gopsutil checks PID existence even when reading a synthetic procfs.
	startProcess := func() int {
		cmd := exec.Command("sleep", "60")
		require.NoError(t, cmd.Start())
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		return cmd.Process.Pid
	}
	rootPid, childPid, grandchildPid := os.Getpid(), startProcess(), startProcess()
	for pid, ppid := range map[int]int{rootPid: 0, childPid: rootPid, grandchildPid: childPid} {
		dir := filepath.Join(root, fmt.Sprint(pid))
		require.NoError(t, os.Mkdir(dir, 0700))
		stat := fmt.Sprintf("%d (test process) S %d%s\n", pid, ppid, strings.Repeat(" 0", 48))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0600))
	}
	// A process whose stat has disappeared must not fail the whole enumeration.
	missingPid := startProcess()
	require.NoError(t, os.Mkdir(filepath.Join(root, fmt.Sprint(missingPid)), 0700))
	// Likewise, skip a process whose stat cannot be read due to permissions.
	inaccessiblePid := startProcess()
	dir := filepath.Join(root, fmt.Sprint(inaccessiblePid))
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stat"), []byte(fmt.Sprintf("%d (inaccessible) S %d%s\n", inaccessiblePid, rootPid, strings.Repeat(" 0", 48))), 0000))
	pids, err := commandutil.ChildPids(os.Getpid())
	require.NoError(t, err)
	require.ElementsMatch(t, []int{childPid}, pids)
}

func TestProcessEnumerationError(t *testing.T) {
	t.Setenv("HOST_PROC", filepath.Join(t.TempDir(), "missing"))
	_, err := commandutil.ChildPids(os.Getpid())
	require.Error(t, err)
}
