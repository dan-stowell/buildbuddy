//go:build (darwin || linux) && !android && !ios

package commandutil_test

import (
	"context"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/commandutil"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/stretchr/testify/assert"
)

// The background process self-terminates so even the failing baseline is bounded.
func TestRun_VerifyExitedRootDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := commandutil.Run(ctx, &repb.Command{
		Arguments: []string{"sh", "-c", "sleep 2 & echo complete"},
	}, t.TempDir(), nil, &interfaces.Stdio{})
	elapsed := time.Since(start)
	t.Logf("elapsed=%s exit=%d error=%v", elapsed, res.ExitCode, res.Error)
	assert.Less(t, elapsed, time.Second, "inherited pipe must not block past the action deadline")
	if elapsed >= 100*time.Millisecond {
		assert.Error(t, res.Error, "deadline must take precedence over main-process success")
	}
}
