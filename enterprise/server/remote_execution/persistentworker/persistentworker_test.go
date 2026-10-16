package persistentworker_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// stalledContainer can stall at each blocking point in a request exchange.
type stalledContainer struct {
	container.CommandContainer
	protocol, phase string
	ready, exited   chan struct{}
}

func (c *stalledContainer) Exec(ctx context.Context, command *repb.Command, stdio *interfaces.Stdio) *interfaces.CommandResult {
	defer close(c.exited)
	if c.phase != "write" {
		if c.protocol == "json" {
			var request json.RawMessage
			if err := json.NewDecoder(stdio.Stdin).Decode(&request); err != nil {
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
		var partial []byte
		if c.phase == "prefix" {
			partial = []byte{0x80}
		}
		if c.phase == "body" {
			partial = []byte{10, 0x12}
		}
		if c.protocol == "json" && c.phase == "body" {
			partial = []byte(`{"output":`)
		}
		if len(partial) > 0 {
			_, _ = stdio.Stdout.Write(partial)
		}
	}
	close(c.ready)
	<-ctx.Done()
	return &interfaces.CommandResult{ExitCode: -1, Error: ctx.Err()}
}

func TestWorkerExec_CancellationUnblocksIO(t *testing.T) {
	for _, protocol := range []string{"proto", "json"} {
		for _, phase := range []string{"write", "response", "prefix", "body"} {
			if protocol == "json" && phase == "prefix" {
				continue
			}
			t.Run(protocol+"/"+phase, func(t *testing.T) {
				ws, err := workspace.New(nil, t.TempDir(), &workspace.Opts{})
				require.NoError(t, err)
				// Exceed the OS pipe capacity so a worker ignoring stdin blocks the write.
				require.NoError(t, os.WriteFile(filepath.Join(ws.Path(), "flags"), []byte(strings.Repeat("x", 512*1024)), 0600))
				cmd := &repb.Command{Arguments: []string{"worker", "@flags"}}
				ctr := &stalledContainer{protocol: protocol, phase: phase, ready: make(chan struct{}), exited: make(chan struct{})}
				w, err := persistentworker.Start(t.Context(), ws, ctr, protocol, cmd)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, w.Stop()) })
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan *interfaces.CommandResult, 1)
				go func() { done <- w.Exec(ctx, cmd) }()
				select {
				case <-ctr.ready:
				case <-time.After(5 * time.Second):
					t.Fatal("worker did not reach stall")
				}
				cancel()
				select {
				case res := <-done:
					require.True(t, status.IsCanceledError(res.Error), "%v", res.Error)
				case <-time.After(2 * time.Second):
					t.Fatal("request ignored cancellation")
				}
				// Exec joins the exchange goroutine, and Stop waits for container exit.
				select {
				case <-ctr.exited:
				default:
					t.Fatal("worker still running")
				}
			})
		}
	}
}
