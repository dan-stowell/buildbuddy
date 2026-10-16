package persistentworker_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/container"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/containers/bare"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/persistentworker"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/workspace"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
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

func TestWorkerStop_UnreadOutputAfterInvalidResponse(t *testing.T) {
	testleak.Check(t)
	testleak.CheckFDs(t)

	ctx := context.Background()
	ctr := bare.NewBareCommandContainer(&bare.Opts{})
	require.NoError(t, ctr.Create(ctx, t.TempDir()))
	// After receiving a request, the worker writes 4000 NUL bytes in a single
	// write (atomic, since it is less than PIPE_BUF) and keeps running. The
	// JSON decoder fails on the first byte without reading the rest, so the
	// stdout copy goroutine is left with unread output.
	script := `head -c 1 >/dev/null; dd if=/dev/zero bs=4000 count=1 2>/dev/null; exec sleep 300`
	cmd := &repb.Command{Arguments: []string{"sh", "-c", script}}
	w, err := persistentworker.Start(ctx, &workspace.Workspace{}, ctr, "json", cmd)
	require.NoError(t, err)

	res := w.Exec(ctx, cmd)
	require.ErrorContains(t, res.Error, "invalid character")

	require.NoError(t, w.Stop())
}

type exitTrackingContainer struct {
	container.CommandContainer
	exited chan struct{}
}

func (c *exitTrackingContainer) Exec(ctx context.Context, cmd *repb.Command, stdio *interfaces.Stdio) *interfaces.CommandResult {
	defer close(c.exited)
	return c.CommandContainer.Exec(ctx, cmd, stdio)
}

// A worker that has started an incomplete response must be killed when the
// request expires, even though its process has a longer-lived context.
func TestWorkerExec_StalledResponseHonorsContext(t *testing.T) {
	testleak.Check(t)
	testleak.CheckFDs(t)
	for range 10 {
		ctx := context.Background()
		ctr := &exitTrackingContainer{CommandContainer: bare.NewBareCommandContainer(&bare.Opts{}), exited: make(chan struct{})}
		require.NoError(t, ctr.Create(ctx, t.TempDir()))
		// Announce a 100-byte proto response, send one byte, then wait on stdin.
		// There are no timers or child processes in the stalled shell.
		cmd := &repb.Command{Arguments: []string{"sh", "-c", "head -c 1 >/dev/null; printf '\\144x'; read ignored"}}
		w, err := persistentworker.Start(ctx, &workspace.Workspace{}, ctr, "proto", cmd)
		require.NoError(t, err)
		runCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		done := make(chan *interfaces.CommandResult, 1)
		go func() { done <- w.Exec(runCtx, cmd) }()
		select {
		case res := <-done:
			require.Error(t, res.Error)
		case <-time.After(5 * time.Second):
			cancel()
			require.NoError(t, w.Stop())
			<-done
			t.Fatal("Exec remained blocked after request deadline")
		}
		cancel()
		// The underlying Exec has waited for and reaped the process.
		select {
		case <-ctr.exited:
		default:
			_ = w.Stop()
			t.Fatal("Exec returned before stopping the worker process")
		}
		// Repeated Stop remains safe after cancellation.
		require.NoError(t, w.Stop())
	}
}
