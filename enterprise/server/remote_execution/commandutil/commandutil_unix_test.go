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
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
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

// A container's signal channel and caller context may outlive an individual
// command. Neither should keep that command's forwarding goroutine alive.
func TestRun_SignalForwarderStopsOnExit(t *testing.T) {
	for _, n := range []int{1, 10, 50} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			testleak.Check(t)
			before := signalForwarderCount()
			fds := commandFDTargets(t)
			signal := make(chan syscall.Signal)
			for range n {
				result := commandutil.RunWithOpts(context.Background(), &repb.Command{Arguments: []string{"sh", "-c", "echo hi; exit 0"}}, &commandutil.RunOpts{Signal: signal})
				require.NoError(t, result.Error)
				require.Equal(t, 0, result.ExitCode)
			}
			// Poll for asynchronous cleanup; no fixed sleep is used as a fix.
			assert.Eventually(t, func() bool { return signalForwarderCount() == before }, time.Second, 10*time.Millisecond)
			after := commandFDTargets(t)
			t.Logf("N=%d forwarders before=%d after=%d FDs before=%d after=%d targets=%v", n, before, signalForwarderCount(), len(fds), len(after), after)
			assert.Equal(t, fds, after)
		})
	}
}

func signalForwarderCount() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "commandutil.forwardSignals(")
}

// Local Linux FD snapshot until testleak has a shared FD detector. Readlink
// excludes the transient descriptor used by ReadDir itself.
func commandFDTargets(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	targets := make(map[string]string)
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		targets[entry.Name()] = target
	}
	return targets
}

func TestRun_RepeatedLifecycleResources(t *testing.T) {
	for _, outcome := range []string{"success", "nonzero", "timeout", "cancel", "killed", "output_limit"} {
		t.Run(outcome, func(t *testing.T) {
			for _, n := range []int{1, 10, 50} {
				t.Run(fmt.Sprint(n), func(t *testing.T) {
					testleak.Check(t)
					before := commandFDTargets(t)
					for range n {
						ctx, cancel := context.WithCancel(context.Background())
						script := "echo hi"
						stdio := &interfaces.Stdio{}
						switch outcome {
						case "nonzero":
							script = "exit 7"
						case "timeout":
							cancel()
							ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
							script = "sleep 100"
						case "cancel":
							script = "echo ready; exec sleep 100"
							stdio.Stdout = cancelOnWrite{cancel: cancel}
						case "killed":
							script = "kill -KILL $$"
						case "output_limit":
							// Exercise the actual output limiter with finite output so the test
							// doesn't depend on a producer ignoring EPIPE forever.
							flags.Set(t, "executor.stdouterr_max_size_bytes", uint64(32))
							stdio.Stdout = commandutil.LimitStdOutErrWriter(io.Discard)
							script = "printf '%0100d' 0"
						}
						result := commandutil.Run(ctx, &repb.Command{Arguments: []string{"sh", "-c", script}}, ".", nil, stdio)
						cancel()
						switch outcome {
						case "success":
							require.NoError(t, result.Error)
							require.Zero(t, result.ExitCode)
						case "nonzero":
							require.NoError(t, result.Error)
							require.Equal(t, 7, result.ExitCode)
						default:
							require.Error(t, result.Error)
						}
					}
					assert.Eventually(t, func() bool { return len(commandFDTargets(t)) == len(before) }, time.Second, 10*time.Millisecond)
					after := commandFDTargets(t)
					t.Logf("outcome=%s N=%d FDs before=%d after=%d targets=%v", outcome, n, len(before), len(after), after)
				})
			}
		})
	}
}

type cancelOnWrite struct{ cancel context.CancelFunc }

func (w cancelOnWrite) Write(p []byte) (int, error) { w.cancel(); return len(p), nil }

// The shell has exited and been reaped before cancellation. Its background
// child still owns the output pipes, so cancellation must kill the process
// group even though the root no longer appears in the process tree.
func TestRun_OrphanedOutputPipes_Cancel(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses Linux /proc to establish root exit and child liveness")
	}
	for _, n := range []int{1, 10, 50} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			testleak.Check(t)
			before := commandFDTargets(t)
			goroutinesBefore := runtime.NumGoroutine()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			results := make(chan *interfaces.CommandResult, n)
			var children []int
			// Always release the controlled children, including on baseline failures.
			defer func() {
				for _, pid := range children {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
				for range n {
					select {
					case <-results:
					case <-time.After(5 * time.Second):
						t.Error("Run did not finish after child cleanup")
						return
					}
				}
			}()
			for i := range n {
				pidFile := filepath.Join(t.TempDir(), fmt.Sprint(i))
				go func() {
					results <- commandutil.Run(ctx, &repb.Command{Arguments: []string{"sh", "-c", `sleep 100 & echo "$$ $!" > "$1"; echo hi`, "sh", pidFile}}, ".", nil, nil)
				}()
				var root int
				require.Eventually(t, func() bool {
					b, err := os.ReadFile(pidFile)
					if err != nil {
						return false
					}
					fields := strings.Fields(string(b))
					if len(fields) != 2 {
						return false
					}
					root, err = strconv.Atoi(fields[0])
					if err != nil {
						return false
					}
					child, err := strconv.Atoi(fields[1])
					if err != nil {
						return false
					}
					children = append(children, child)
					return true
				}, 5*time.Second, time.Millisecond)
				require.Eventually(t, func() bool { _, err := os.Stat(fmt.Sprintf("/proc/%d", root)); return os.IsNotExist(err) }, 5*time.Second, time.Millisecond)
			}
			cancel()
			assert.Eventually(t, func() bool { return len(results) == n }, time.Second, 10*time.Millisecond, "Run must finish after cancellation even when the root was already reaped")
			after := commandFDTargets(t)
			t.Logf("N=%d returned=%d/%d goroutines before=%d after=%d FDs before=%d after=%d targets=%v", n, len(results), n, goroutinesBefore, runtime.NumGoroutine(), len(before), len(after), after)
			if n == 1 && len(results) != n {
				buf := make([]byte, 1<<20)
				size := runtime.Stack(buf, true)
				t.Logf("blocked command stacks:\n%s", buf[:size])
			}
			assert.Equal(t, before, after)
			live := 0
			for _, pid := range children {
				b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				// Zombies have released their FDs and await the sandbox init's reaper.
				if err == nil && !strings.Contains(string(b), ") Z ") {
					live++
				}
			}
			assert.Zero(t, live, "background children still running after cancellation")
		})
	}
}
