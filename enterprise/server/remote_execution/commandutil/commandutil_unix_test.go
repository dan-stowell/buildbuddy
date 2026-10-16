//go:build (darwin || linux) && !android && !ios

package commandutil_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/commandutil"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// set by x_defs in BUILD file
var testBinaryRunfilePath string

func TestRun_NormalExit_NoError(t *testing.T) {
	ctx := context.Background()

	for _, code := range []int{0, 1, 137} {
		res := runSh(ctx, fmt.Sprintf("exit %d", code))

		assert.NoError(t, res.Error)
		assert.Equal(t, code, res.ExitCode)
		assert.Greater(t, res.UsageStats.GetCpuNanos(), int64(0))
		assert.Less(t, time.Duration(res.UsageStats.GetCpuNanos()), 10*time.Millisecond, "sh -c 'exit {code}' should not consume a lot of CPU time")
		assert.Greater(t, res.UsageStats.GetPeakMemoryBytes(), int64(0))
		assert.Less(t, res.UsageStats.GetPeakMemoryBytes(), int64(1e6), "sh -c 'exit {code}' should not consume a lot of memory")
	}
}

func TestRun_Stdio(t *testing.T) {
	ctx := context.Background()
	cmd := &repb.Command{Arguments: []string{"sh", "-c", `
		if ! [ "$(cat)" = "TestInput" ] ; then
			echo "ERROR: missing expected TestInput on stdin"
			exit 1
		fi
		echo TestOutput
		echo TestError >&2
	`}}
	var stdout, stderr bytes.Buffer
	res := commandutil.Run(ctx, cmd, ".", nil /*=statsListener*/, &interfaces.Stdio{
		Stdin:  strings.NewReader("TestInput\n"),
		Stdout: &stdout,
		Stderr: &stderr,
	})

	assert.NoError(t, res.Error)
	assert.Equal(t, "TestOutput\n", stdout.String(), "stdout opt should be respected")
	assert.Empty(t, string(res.Stdout), "stdout in command result should be empty when stdout opt is specified")
	assert.Equal(t, "TestError\n", stderr.String(), "stderr opt should be respected")
	assert.Empty(t, string(res.Stderr), "stderr in command result should be empty when stderr opt is specified")
}

func TestRun_Stdio_StdinNotConsumedByCommand(t *testing.T) {
	ctx := context.Background()
	// Note: this command does not consume stdin. We should still be able to write
	// stdin to it, and not have it fail/hang. See
	// https://go.dev/play/p/DpKaVrx8d8G for an example implementation that does
	// not handle this correctly.
	cmd := &repb.Command{Arguments: []string{"echo", "foo"}}

	inr, inw := io.Pipe()
	outr, outw := io.Pipe()

	go func() {
		defer inr.Close()
		defer outw.Close()

		res := commandutil.Run(ctx, cmd, ".", nil /*=statsListener*/, &interfaces.Stdio{
			Stdin:  inr,
			Stdout: outw,
		})
		assert.Empty(t, res.Stdout)
	}()

	inw.Write([]byte("foo"))
	out, err := io.ReadAll(outr)
	require.NoError(t, err)
	assert.Equal(t, "foo\n", string(out))
}

// TODO(bduffany): Treat SIGABRT as a normal exit rather than an unexpected
// termination, and ensure that unexpected terminations are retried rather than
// immediately reporting them to Bazel.

func TestRun_Killed_ErrorResult(t *testing.T) {
	ctx := context.Background()

	{
		res := runSh(ctx, "kill -ABRT $$")

		require.Error(t, res.Error)
		assert.Contains(t, res.Error.Error(), fmt.Sprintf("signal: %s", syscall.SIGABRT))
		assert.Equal(t, -1, res.ExitCode)
	}
	{
		res := runSh(ctx, "kill -INT $$")

		require.Error(t, res.Error)
		assert.Contains(t, res.Error.Error(), "signal: interrupt")
		assert.Equal(t, -1, res.ExitCode)
	}
	{
		res := runSh(ctx, "kill -TERM $$")

		require.Error(t, res.Error)
		assert.Contains(t, res.Error.Error(), "signal: terminated")
		assert.Equal(t, -1, res.ExitCode)
	}
	{
		res := runSh(ctx, "kill -KILL $$")

		require.Error(t, res.Error)
		assert.Contains(t, res.Error.Error(), "signal: killed")
		assert.Equal(t, -1, res.ExitCode)
	}
}

// TODO(bduffany): All the error codes when the command is not found or not
// executable should probably have the same error code, instead of a mix of
// NOT_FOUND / UNAVAILABLE. Also we should probably ensure that Bazel doesn't
// retry these errors at the executor level, since retrying is unlikely to
// help (these errors are most likely due to an incorrect input specification or
// container-image that doesn't contain the executable in PATH)

func TestRun_CommandNotFound_ErrorResult(t *testing.T) {
	ctx := context.Background()

	{
		cmd := &repb.Command{Arguments: []string{"./command_not_found_in_working_dir"}}
		res := commandutil.Run(ctx, cmd, ".", nil /*=statsListener*/, &interfaces.Stdio{})

		assert.Error(t, res.Error)
		assert.True(
			t, status.IsUnavailableError(res.Error),
			"expecting UNAVAILABLE when executable file is missing, got: %s", res.Error)
		assert.Equal(t, -2, res.ExitCode)
	}
	{
		cmd := &repb.Command{Arguments: []string{"command_not_found_in_PATH"}}
		res := commandutil.Run(ctx, cmd, ".", nil /*=statsListener*/, &interfaces.Stdio{})

		assert.Error(t, res.Error)
		assert.True(
			t, status.IsNotFoundError(res.Error),
			"expecting NOT_FOUND when executable is not found in $PATH, got: %s", res.Error)
		assert.Equal(t, -2, res.ExitCode)
	}
}

func TestRun_CommandNotExecutable_ErrorResult(t *testing.T) {
	ctx := context.Background()
	wd := testfs.MakeTempDir(t)
	testfs.WriteAllFileContents(t, wd, map[string]string{"non_executable_file": ""})

	cmd := &repb.Command{Arguments: []string{"./non_executable_file"}}
	res := commandutil.Run(ctx, cmd, wd, nil /*=statsListener*/, &interfaces.Stdio{})

	assert.Error(t, res.Error)
	assert.True(
		t, status.IsUnavailableError(res.Error),
		"expecting UNAVAILABLE when command is not executable, got: %s", res.Error)
	assert.Equal(t, -2, res.ExitCode)
}

func TestRun_Timeout(t *testing.T) {
	ctx := context.Background()
	wd := testfs.MakeTempDir(t)

	cmd := &repb.Command{Arguments: []string{"sh", "-c", `
		echo stdout >&1
		echo stderr >&2
		sleep 60
	`}}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	res := commandutil.Run(ctx, cmd, wd, nil /*=statsListener*/, &interfaces.Stdio{})

	require.True(t, status.IsDeadlineExceededError(res.Error), "expected DeadlineExceeded but got: %s", res.Error)
	assert.Equal(t, "stdout\n", string(res.Stdout))
	assert.Equal(t, "stderr\n", string(res.Stderr))
}

func TestRun_SubprocessInOwnProcessGroup_Timeout(t *testing.T) {
	ctx := context.Background()
	wd := testfs.MakeTempDir(t)

	cmd := &repb.Command{Arguments: []string{
		"sh", "-c",
		// Spawn a nested process inside the shell which becomes its own process group leader.
		// Using a go binary here because sh does not have a straightforward
		// way to make the setpgid system call.
		testfs.RunfilePath(t, testBinaryRunfilePath),
	}}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	res := commandutil.Run(ctx, cmd, wd, nil /*=statsListener*/, &interfaces.Stdio{})

	assert.True(t, status.IsDeadlineExceededError(res.Error), "expected DeadlineExceeded but got: %s", res.Error)
	assert.Equal(t, "stdout\n", string(res.Stdout))
	assert.Equal(t, "stderr\n", string(res.Stderr))
}

func TestRun_EnableStats_RecordsMemoryStats(t *testing.T) {
	cmd := &repb.Command{Arguments: []string{
		"python3", "-c", useMemPythonScript(500e6, 2*time.Second),
	}}

	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res := commandutil.Run(ctx, cmd, ".", nopStatsListener, &interfaces.Stdio{})

	require.Equal(t, "", string(res.Stderr))
	require.Equal(t, 0, res.ExitCode)
	require.GreaterOrEqual(
		t, res.UsageStats.PeakMemoryBytes, int64(500e6),
		"expected at least 500MB of memory usage")
	require.LessOrEqual(
		t, res.UsageStats.PeakMemoryBytes, int64(550e6),
		"expected not much more than 500MB of memory usage")
}

func TestRun_EnableStats_RecordsCPUStats(t *testing.T) {
	cmd := &repb.Command{Arguments: []string{
		"python3", "-c", useCPUPythonScript(3 * time.Second),
	}}

	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res := commandutil.Run(ctx, cmd, ".", nopStatsListener, &interfaces.Stdio{})

	require.Equal(t, "", string(res.Stderr))
	require.Equal(t, 0, res.ExitCode)
	require.GreaterOrEqual(
		t, res.UsageStats.GetCpuNanos(), int64(500e6),
		"expected around 3s of CPU usage")
	require.LessOrEqual(
		t, res.UsageStats.GetCpuNanos(), int64(4e9),
		"expected around 3s of CPU usage")
}

func TestRun_EnableStats_ComplexProcessTree_RecordsStatsFromAllChildren(t *testing.T) {
	workDir := testfs.MakeTempDir(t)
	testfs.WriteAllFileContents(t, workDir, map[string]string{
		"cpu1.py": useCPUPythonScript(3 * time.Second),
		"cpu2.py": useCPUPythonScript(1 * time.Second),
		"mem1.py": useMemPythonScript(500e6, 3*time.Second),
		"mem2.py": useMemPythonScript(250e6, 2*time.Second),
	})
	// Run 3 python processes concurrently, staggering them a bit to  make sure
	// we measure over time. Wrap some with 'sh -c' to test that we can handle
	// grandchildren as well.
	cmd := &repb.Command{Arguments: []string{"bash", "-c", `
		python3 ./cpu1.py &
		sleep 0.1
		sh -c 'python3 ./mem1.py' &
		sleep 0.1
		sh -c 'python3 ./cpu2.py' &
		sleep 0.1
		python3 ./mem2.py &
		wait
		`,
	}}

	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res := commandutil.Run(ctx, cmd, workDir, nopStatsListener, &interfaces.Stdio{})

	require.Equal(t, "", string(res.Stderr))
	require.Equal(t, 0, res.ExitCode)
	require.GreaterOrEqual(
		t, res.UsageStats.GetCpuNanos(), int64(1e9),
		"expected around 4s of CPU usage")
	require.LessOrEqual(
		t, res.UsageStats.GetCpuNanos(), int64(5e9),
		"expected around 4s of CPU usage")
	require.GreaterOrEqual(
		t, res.UsageStats.GetPeakMemoryBytes(), int64(750e6),
		"expected at least 750MB peak memory")
	require.LessOrEqual(
		t, res.UsageStats.GetPeakMemoryBytes(), int64(850e6),
		"expected not much more than 750MB peak memory")
}

// Background children that inherit the output pipes must not keep Run blocked
// after cancellation, even when they are no longer descendants of the root
// process (because the root, or an intermediate parent, already exited).
func TestRun_CancelKillsOrphanedChildHoldingOutputPipes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses /proc")
	}
	for _, tc := range []struct {
		name      string
		script    string
		rootExits bool
	}{
		// The root exits and is reaped before cancellation.
		{"root_reaped", `sleep 100 & echo "$$ $!" > "$1"`, true},
		// The root is alive at cancellation, but the child was reparented.
		{"child_reparented", `(sleep 100 & echo "$$ $!" > "$1"); sleep 100`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testleak.Check(t)
			pipesBefore := pipeFDCount(t)
			pidFile := filepath.Join(testfs.MakeTempDir(t), "pids")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan *interfaces.CommandResult, 1)
			go func() {
				done <- commandutil.Run(ctx, &repb.Command{Arguments: []string{"sh", "-c", tc.script, "sh", pidFile}}, ".", nil, nil)
			}()
			var root, child int
			require.Eventually(t, func() bool {
				b, err := os.ReadFile(pidFile)
				if err != nil {
					return false
				}
				_, err = fmt.Sscanf(string(b), "%d %d\n", &root, &child)
				return err == nil
			}, 10*time.Second, time.Millisecond)
			// On failure, release the child so that Run can return. (On
			// success it was already killed; its pid may have been reused.)
			defer func() {
				if t.Failed() {
					cancel()
					_ = syscall.Kill(child, syscall.SIGKILL)
					<-done
				}
			}()
			// Wait until the child can no longer be found by walking the
			// process tree from the root.
			require.Eventually(t, func() bool {
				if tc.rootExits {
					if _, err := os.Stat(fmt.Sprintf("/proc/%d", root)); !os.IsNotExist(err) {
						return false
					}
				}
				for pid := child; pid > 1; pid = parentPid(pid) {
					if pid == root {
						return false
					}
				}
				return true
			}, 10*time.Second, time.Millisecond)

			cancel()
			select {
			case res := <-done:
				done <- res
			case <-time.After(10 * time.Second):
				require.FailNow(t, "Run did not return after cancellation; orphaned child still holds output pipes")
			}
			assert.Equal(t, pipesBefore, pipeFDCount(t), "pipe FDs leaked")
		})
	}
}

// parentPid returns the parent pid of pid, or -1 if it can't be read.
func parentPid(pid int) int {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return -1
	}
	s := string(b)
	fields := strings.Fields(s[strings.LastIndex(s, ")")+1:])
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return -1
	}
	return ppid
}

func pipeFDCount(t *testing.T) int {
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	n := 0
	for _, e := range entries {
		if target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); err == nil && strings.HasPrefix(target, "pipe:") {
			n++
		}
	}
	return n
}

// A container's signal channel and caller context may outlive an individual
// command; the command's signal forwarding goroutine must not.
func TestRun_SignalForwarderExitsWithCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Registered before testleak.Check, so it runs after the leak check.
	t.Cleanup(cancel)
	testleak.Check(t)
	signal := make(chan syscall.Signal, 1)
	for range 10 {
		res := commandutil.RunWithOpts(ctx, &repb.Command{Arguments: []string{"true"}}, &commandutil.RunOpts{Signal: signal})
		require.NoError(t, res.Error)
	}
}
