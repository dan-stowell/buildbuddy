package persistentworker_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"testing"
	"unicode/utf8"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/container"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/persistentworker"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/workspace"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
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

// cancelBeforeResponseContainer makes cancellation happen before a complete
// successful response is available. No timer or scheduler ordering is required.
type cancelBeforeResponseContainer struct {
	container.CommandContainer
	protocol string
	cancel   context.CancelFunc
}

func (c *cancelBeforeResponseContainer) Exec(ctx context.Context, command *repb.Command, stdio *interfaces.Stdio) *interfaces.CommandResult {
	if c.protocol == "json" {
		var req json.RawMessage
		if err := json.NewDecoder(stdio.Stdin).Decode(&req); err != nil {
			return &interfaces.CommandResult{Error: err}
		}
	} else {
		r := bufio.NewReader(stdio.Stdin)
		n, err := binary.ReadUvarint(r)
		if err != nil {
			return &interfaces.CommandResult{Error: err}
		}
		if _, err := io.CopyN(io.Discard, r, int64(n)); err != nil {
			return &interfaces.CommandResult{Error: err}
		}
	}
	c.cancel()
	if c.protocol == "json" {
		_, _ = io.WriteString(stdio.Stdout, "{\"exitCode\":0}\n")
	} else {
		// An empty WorkResponse is a successful response, framed with size 0.
		_, _ = stdio.Stdout.Write([]byte{0})
	}
	return &interfaces.CommandResult{ExitCode: 0}
}

func TestWorkerExec_CancellationPrecedesSuccessfulResponse(t *testing.T) {
	for _, protocol := range []string{"proto", "json"} {
		t.Run(protocol, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctr := &cancelBeforeResponseContainer{protocol: protocol, cancel: cancel}
			cmd := &repb.Command{Arguments: []string{"worker"}}
			w, err := persistentworker.Start(t.Context(), &workspace.Workspace{}, ctr, protocol, cmd)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, w.Stop()) })
			res := w.Exec(ctx, cmd)
			require.True(t, status.IsCanceledError(res.Error), "successful response after cancellation: %+v", res)
			require.Less(t, res.ExitCode, 0)
		})
	}
}
