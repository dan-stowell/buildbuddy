//go:build (darwin || linux) && !android && !ios

package commandutil_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/commandutil"
	"github.com/stretchr/testify/require"
)

// Exercise both large shared output and a consumer slower than the drain grace.
// There is no cancellation, so every byte must be delivered in descriptor order.
func TestV4ReviewSharedLargeSlowSuccess(t *testing.T) {
	w := &reviewSlowWriter{}
	cmd := exec.Command("sh", "-c", `chunk=`+strings.Repeat("x", 4096)+`; i=0; while [ "$i" -lt 256 ]; do printf '%sO' "$chunk"; printf '%sE' "$chunk" >&2; i=$((i+1)); done`)
	cmd.Stdout, cmd.Stderr = w, w
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := commandutil.RunWithProcessTreeCleanup(ctx, cmd, nil)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat(strings.Repeat("x", 4096)+"O"+strings.Repeat("x", 4096)+"E", 256), w.String())
}

type reviewSlowWriter struct {
	bytes.Buffer
	delayed bool
}

func (w *reviewSlowWriter) Write(p []byte) (int, error) {
	if !w.delayed {
		w.delayed = true
		time.Sleep(1500 * time.Millisecond)
	}
	return w.Buffer.Write(p)
}

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

func TestV4ReviewFileAndNilDestinations(t *testing.T) {
	for _, useFile := range []bool{false, true} {
		t.Run(map[bool]string{false: "nil", true: "file"}[useFile], func(t *testing.T) {
			cmd := exec.Command("sh", "-c", "printf out; printf err >&2")
			var f *os.File
			if useFile {
				var err error
				f, err = os.CreateTemp(t.TempDir(), "output")
				require.NoError(t, err)
				defer f.Close()
				cmd.Stdout, cmd.Stderr = f, f
			}
			_, err := commandutil.RunWithProcessTreeCleanup(context.Background(), cmd, nil)
			require.NoError(t, err)
			if useFile {
				b, err := os.ReadFile(f.Name())
				require.NoError(t, err)
				require.Equal(t, "outerr", string(b))
			}
		})
	}
}
