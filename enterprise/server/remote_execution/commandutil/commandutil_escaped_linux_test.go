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
	assert.Less(t, elapsed, 4*time.Second, "escaped pipe holder must not extend execution indefinitely past deadline")
}
