//go:build (darwin || linux) && !android && !ios

package commandutil_test

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/commandutil"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/stretchr/testify/assert"
)

// A shared destination must preserve the order of writes to fd 1 and fd 2,
// as exec.Cmd does by connecting both descriptors to the same pipe.
func TestRun_ReviewSharedOutputOrder(t *testing.T) {
	var output bytes.Buffer
	cmd := exec.Command("sh", "-c", `i=0; while [ "$i" -lt 2000 ]; do printf 'out-%s\n' "$i"; printf 'err-%s\n' "$i" >&2; i=$((i+1)); done`)
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := commandutil.RunWithProcessTreeCleanup(ctx, cmd, nil)
	assert.NoError(t, err)
	var expected strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&expected, "out-%d\nerr-%d\n", i, i)
	}
	got := output.String()
	if got != expected.String() {
		// Keep failure output bounded while distinguishing reordering from loss.
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		t.Fatalf("combined stdout/stderr order changed: got %d bytes, %d lines, want %d bytes; prefix=%q", len(got), len(lines), expected.Len(), got[:min(160, len(got))])
	}
}

// Cancel only after a child has inherited both pipes and the root has exited.
// The child self-terminates, bounding a failure against the original code.
func TestRun_ReviewCancelAfterRootExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := commandutil.Run(ctx, &repb.Command{Arguments: []string{"sh", "-c", "sleep 4 & echo out; echo err >&2"}}, t.TempDir(), nil, nil)
	elapsed := time.Since(start)
	t.Logf("deterministic script; elapsed=%s exit=%d error=%v", elapsed, res.ExitCode, res.Error)
	assert.Equal(t, "out\n", string(res.Stdout))
	assert.Equal(t, "err\n", string(res.Stderr))
	assert.Error(t, res.Error)
	assert.Less(t, elapsed, 2*time.Second)
}
