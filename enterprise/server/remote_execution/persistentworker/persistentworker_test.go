package persistentworker_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/containers/bare"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"testing"
	"unicode/utf8"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/container"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/persistentworker"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/workspace"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

type stderrContainer struct {
	container.CommandContainer
	stderr []byte
}

func (c *stderrContainer) Exec(ctx context.Context, command *repb.Command, stdio *interfaces.Stdio) *interfaces.CommandResult {
	// Wait for a complete work request before exiting so that the stderr is
	// associated with the current request rather than the worker startup.
	r := bufio.NewReader(stdio.Stdin)
	size, err := binary.ReadUvarint(r)
	if err == nil {
		_, err = io.CopyN(io.Discard, r, int64(size))
	}
	if err != nil {
		return &interfaces.CommandResult{Error: err, ExitCode: -2}
	}
	_, _ = stdio.Stderr.Write(c.stderr)
	return &interfaces.CommandResult{ExitCode: 1}
}

func TestWorkerExec_Stderr(t *testing.T) {
	tests := []struct {
		name       string
		stderr     []byte
		wantStderr string
	}{
		{
			name:       "empty",
			wantStderr: "<empty>",
		},
		{
			name:       "valid UTF-8",
			stderr:     []byte("hello, 世界"),
			wantStderr: "hello, 世界",
		},
		{
			name:       "invalid UTF-8",
			stderr:     []byte{'a', 0xff, 'b'},
			wantStderr: "a�b",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			cmd := &repb.Command{Arguments: []string{"worker"}}
			ctr := &stderrContainer{stderr: test.stderr}
			w, err := persistentworker.Start(ctx, &workspace.Workspace{}, ctr, "proto", cmd)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, w.Stop())
			})

			result := w.Exec(ctx, cmd)
			require.Error(t, result.Error)
			require.True(t, utf8.ValidString(result.Error.Error()), "error contains invalid UTF-8: %q", result.Error)
			require.Contains(t, result.Error.Error(), "persistent worker stderr:\n"+test.wantStderr)
		})
	}
}

// After a malformed response the caller abandons stdout and removes the runner.
// Output already queued behind that response must not keep Exec alive.
func TestWorkerStopUnreadOutput(t *testing.T) {
	testleak.Check(t)
	testleak.CheckFDs(t)
	for _, n := range []int{1, 10, 50} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			beforeG := runtime.NumGoroutine()
			beforeFD, err := os.ReadDir("/proc/self/fd")
			require.NoError(t, err)
			testleak.CheckGrowth(t, testleak.Growth{Iterations: n}, func(i int) {
				ctx := context.Background()
				ctr := bare.NewBareCommandContainer(&bare.Opts{})
				require.NoError(t, ctr.Create(ctx, t.TempDir()))
				// The shell is the worker itself (no descendants), and writes enough
				// trailing data that the stdout copier cannot finish before Stop.
				cmd := &repb.Command{Arguments: []string{"/bin/sh", "-c", `read request; printf 'garbage\n'; while :; do printf 'trailing worker output\n'; done`}}
				w, err := persistentworker.Start(ctx, &workspace.Workspace{}, ctr, "json", cmd)
				require.NoError(t, err)
				require.Error(t, w.Exec(ctx, cmd).Error)
				err = w.Stop()
				require.NoError(t, err)
				require.NoError(t, ctr.Remove(ctx))
			})
			afterFD, err := os.ReadDir("/proc/self/fd")
			require.NoError(t, err)
			t.Logf("N=%d post-stop goroutines=%d -> %d FDs=%d -> %d", n, beforeG, runtime.NumGoroutine(), len(beforeFD), len(afterFD))
		})
	}
}

// Healthy workers stay alive across requests; only sample after Stop.
func TestWorkerLifecycle(t *testing.T) {
	testleak.Check(t)
	testleak.CheckFDs(t)
	for _, mode := range []string{"healthy", "crash", "idle-exit"} {
		for _, n := range []int{1, 10, 50} {
			t.Run(fmt.Sprintf("%s/%d", mode, n), func(t *testing.T) {
				beforeG := runtime.NumGoroutine()
				beforeFD, err := os.ReadDir("/proc/self/fd")
				require.NoError(t, err)
				testleak.CheckGrowth(t, testleak.Growth{Iterations: n}, func(i int) {
					ctx := context.Background()
					ctr := bare.NewBareCommandContainer(&bare.Opts{})
					dir := t.TempDir()
					require.NoError(t, ctr.Create(ctx, dir))
					script := `echo $$ > worker.pid; while read request; do printf '{}\n'; done`
					if mode == "crash" {
						script = `echo $$ > worker.pid; read request; exit 7`
					}
					if mode == "idle-exit" {
						script = `echo $$ > worker.pid; read request; printf '{}\n'; exit 0`
					}
					cmd := &repb.Command{Arguments: []string{"/bin/sh", "-c", script}}
					w, err := persistentworker.Start(ctx, &workspace.Workspace{}, ctr, "json", cmd)
					require.NoError(t, err)
					for range 10 {
						res := w.Exec(ctx, cmd)
						if mode == "healthy" {
							require.NoError(t, res.Error)
						} else {
							break
						}
					}
					require.NoError(t, w.Stop())
					require.NoError(t, ctr.Remove(ctx))
					data, err := os.ReadFile(dir + "/worker.pid")
					require.NoError(t, err)
					var pid int
					_, err = fmt.Sscan(string(data), &pid)
					require.NoError(t, err)
					_, err = os.Stat(fmt.Sprintf("/proc/%d", pid))
					require.True(t, os.IsNotExist(err), "worker %d still alive", pid)
				})
				afterFD, err := os.ReadDir("/proc/self/fd")
				require.NoError(t, err)
				t.Logf("N=%d mode=%s post-stop goroutines=%d -> %d FDs=%d -> %d", n, mode, beforeG, runtime.NumGoroutine(), len(beforeFD), len(afterFD))
			})
		}
	}
}
