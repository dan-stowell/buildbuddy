//go:build (darwin || linux) && !android && !ios

package commandutil_test

import (
	"bytes"
	"context"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/commandutil"
	"github.com/stretchr/testify/require"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// A finite inherited pipe holder bounds this test even on the unmodified baseline.
func TestV4ReviewSharedOrphanDeadline(t *testing.T) {
	var output bytes.Buffer
	cmd := exec.Command("sh", "-c", "sleep 4 & printf out; printf err >&2")
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := commandutil.RunWithProcessTreeCleanup(ctx, cmd, nil)
	elapsed := time.Since(start)
	code, statusErr := commandutil.ExitCode(ctx, cmd, err)
	t.Logf("deterministic; elapsed=%s exit=%d error=%v", elapsed, code, statusErr)
	require.Less(t, elapsed, 3*time.Second)
	require.Error(t, statusErr)
	require.Equal(t, "outerr", output.String())
}
