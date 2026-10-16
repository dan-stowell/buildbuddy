package procstats_test

import (
	"bufio"
	"os/exec"
	"runtime"
	"runtime/debug"
	"syscall"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/procstats"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/stretchr/testify/require"
)

// Sampling a process tree must not leave file descriptors open (e.g. pidfds
// from os.FindProcess) for the GC to close later.
func TestTreeStatsUpdateDoesNotRetainFDs(t *testing.T) {
	// A shell with 3 sleeping children, so each Update samples 4 processes.
	cmd := exec.Command("/bin/sh", "-c", "sleep 300 & sleep 300 & sleep 300 & echo ready; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", line)

	// Disable GC so that anything only released by a GC cleanup shows up.
	runtime.GC()
	defer debug.SetGCPercent(debug.SetGCPercent(-1))

	ts := procstats.NewTreeStats(cmd.Process.Pid)
	testleak.CheckGrowth(t, testleak.Growth{Iterations: 20}, func(int) {
		require.NoError(t, ts.Update())
		require.Len(t, ts.CPUNanosByPid, 4)
	})
}
