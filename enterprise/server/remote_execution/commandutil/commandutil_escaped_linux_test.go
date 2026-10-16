//go:build linux && !android

package commandutil_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/commandutil"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The child creates a new session before signalling readiness. The root then
// exits, so neither a root-based tree walk nor the original group can find it.
// Self-termination bounds the test even if cancellation fails to close pipes.
func TestRun_EscapedSessionPipeDeadline(t *testing.T) {
	require.FileExists(t, "/usr/bin/setsid")
	_, err := exec.LookPath("sh")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	res := commandutil.Run(ctx, &repb.Command{Arguments: []string{"sh", "-c",
		`/usr/bin/setsid sh -c 'echo ready > ready; exec sleep 6' & while [ ! -f ready ]; do sleep 0.01; done; echo main-exited`}}, t.TempDir(), nil, &interfaces.Stdio{})
	elapsed := time.Since(start)
	t.Logf("elapsed=%s deadline=2s child-lifetime=6s exit=%d error=%v stdout=%q", elapsed, res.ExitCode, res.Error, res.Stdout)
	assert.Equal(t, "main-exited\n", string(res.Stdout))
	assert.True(t, status.IsDeadlineExceededError(res.Error), "expected deadline status: %v", res.Error)
	require.Error(t, res.Error)
	assert.Contains(t, res.Error.Error(), "stdout/stderr may be truncated")
	assert.Less(t, elapsed, 4*time.Second, "escaped pipe holder must not extend execution indefinitely past deadline")
}

// Exercise explicit cancellation, both output pipes, and a non-default grace.
func TestRun_EscapedSessionPipeCancellation(t *testing.T) {
	require.FileExists(t, "/usr/bin/setsid")
	flags.Set(t, "executor.output_drain_timeout", 200*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(2*time.Second, cancel)
	defer timer.Stop()
	start := time.Now()
	res := commandutil.Run(ctx, &repb.Command{Arguments: []string{"sh", "-c",
		`/usr/bin/setsid sh -c 'echo ready > ready; exec sleep 6' & while [ ! -f ready ]; do sleep 0.01; done; echo stdout; echo stderr >&2`}}, t.TempDir(), nil, nil)
	elapsed := time.Since(start)
	t.Logf("elapsed=%s cancel=2s grace=200ms error=%v", elapsed, res.Error)
	assert.Equal(t, "stdout\n", string(res.Stdout))
	assert.Equal(t, "stderr\n", string(res.Stderr))
	assert.Equal(t, commandutil.KilledExitCode, res.ExitCode)
	require.True(t, status.IsCanceledError(res.Error), "%v", res.Error)
	assert.Contains(t, res.Error.Error(), "stdout/stderr may be truncated")
	assert.Less(t, elapsed, 3500*time.Millisecond)
}

// Exercise cancellation while the root is still alive, and after output EOF.
// The escaped child self-terminates so this is also bounded on the baseline.
func TestRun_ReviewCancellationOrderings(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		truncated    bool
	}{
		{"cancel_before_root_exit", `/usr/bin/setsid sh -c 'echo ready > ready; exec sleep 4' & while [ ! -f ready ]; do sleep 0.01; done; echo out; echo err >&2; sleep 4`, true},
		{"eof_before_cancel", `echo out; echo err >&2; exec 1>/dev/null 2>/dev/null; exec sleep 4`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags.Set(t, "executor.output_drain_timeout", 100*time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			start := time.Now()
			res := commandutil.Run(ctx, &repb.Command{Arguments: []string{"sh", "-c", tc.script}}, t.TempDir(), nil, nil)
			elapsed := time.Since(start)
			t.Logf("deterministic script; elapsed=%s error=%v", elapsed, res.Error)
			assert.Equal(t, "out\n", string(res.Stdout))
			assert.Equal(t, "err\n", string(res.Stderr))
			require.True(t, status.IsDeadlineExceededError(res.Error), "%v", res.Error)
			assert.Less(t, elapsed, 2500*time.Millisecond)
			if tc.truncated {
				assert.Contains(t, res.Error.Error(), "stdout/stderr may be truncated")
			} else {
				assert.NotContains(t, res.Error.Error(), "truncated")
			}
		})
	}
}
