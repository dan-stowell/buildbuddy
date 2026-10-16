//go:build (darwin || linux) && !android && !ios

package commandutil_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/commandutil"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// All background processes spawned by these tests exit on their own after
// bgSleepSeconds, so a failing run never leaves processes behind for long.
const bgSleepSeconds = 20

// readPid waits for a pid file written by the command and returns its value.
func readPid(t *testing.T, path string) int {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && strings.HasSuffix(string(b), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			require.NoError(t, err)
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.FailNow(t, "pid file not written", path)
	return 0
}

func pidAlive(pid int) bool {
	// Signal 0 checks existence. A zombie still "exists", so also check
	// /proc state where available.
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	// Format: pid (comm) S ...
	s := string(b)
	i := strings.LastIndex(s, ")")
	if i >= 0 && i+2 < len(s) && s[i+2] == 'Z' {
		return false
	}
	return true
}

func killOnCleanup(t *testing.T, pidFile string) {
	t.Cleanup(func() {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
}

// The main process exits immediately with status 0 but leaves a background
// child holding the inherited stdout/stderr pipes. The action has a short
// timeout; Run should not block until the background child exits, and must
// not report a successful exit after the deadline has passed.
func TestRun_BackgroundChildHoldsStdout_Timeout(t *testing.T) {
	wd := testfs.MakeTempDir(t)
	pidFile := filepath.Join(wd, "bg.pid")
	killOnCleanup(t, pidFile)
	cmd := &repb.Command{Arguments: []string{"sh", "-c",
		`sh -c 'echo $$ > bg.pid; exec sleep ` + strconv.Itoa(bgSleepSeconds) + `' &
		echo hi`}}

	const timeout = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	res := commandutil.Run(ctx, cmd, wd, nil /*=statsListener*/, &interfaces.Stdio{})
	elapsed := time.Since(start)
	pid := readPid(t, pidFile)
	alive := pidAlive(pid)
	t.Logf("elapsed=%s exitCode=%d err=%v stdout=%q bgPid=%d bgAliveAfterRun=%t", elapsed, res.ExitCode, res.Error, res.Stdout, pid, alive)

	assert.Less(t, elapsed, timeout+5*time.Second, "Run should return shortly after the action timeout, not when the background child exits")
	if elapsed > timeout {
		assert.Error(t, res.Error, "Run returned after the action deadline; it should not report success")
	}
}

// Same as above but with no deadline at all: documents how long Run blocks
// after the main process has exited.
func TestRun_BackgroundChildHoldsStdout_NoTimeout(t *testing.T) {
	wd := testfs.MakeTempDir(t)
	pidFile := filepath.Join(wd, "bg.pid")
	killOnCleanup(t, pidFile)
	cmd := &repb.Command{Arguments: []string{"sh", "-c",
		`sh -c 'echo $$ > bg.pid; exec sleep 5' &
		echo hi`}}

	start := time.Now()
	res := commandutil.Run(context.Background(), cmd, wd, nil /*=statsListener*/, &interfaces.Stdio{})
	elapsed := time.Since(start)
	t.Logf("elapsed=%s exitCode=%d err=%v stdout=%q", elapsed, res.ExitCode, res.Error, res.Stdout)
	assert.Less(t, elapsed, 2*time.Second, "successful root must not wait for background child")
	assert.Equal(t, "hi\n", string(res.Stdout))
	assert.Equal(t, 0, res.ExitCode)
	// Successful bare commands retain background processes for compatibility
	// with callers intentionally keeping daemons between actions.
	pid := readPid(t, pidFile)
	assert.True(t, pidAlive(pid), "successful commands should not kill background processes")
	require.NoError(t, res.Error)
}

// A grandchild is orphaned (its parent subshell exits right away), so it is
// reparented away from the action's process tree. It still holds stdout. The
// main process keeps running past the action timeout. After the timeout kill,
// Run should return promptly and the orphan should be dead.
func TestRun_OrphanedGrandchildHoldsStdout_Timeout(t *testing.T) {
	wd := testfs.MakeTempDir(t)
	pidFile := filepath.Join(wd, "bg.pid")
	killOnCleanup(t, pidFile)
	cmd := &repb.Command{Arguments: []string{"sh", "-c",
		`( sh -c 'echo $$ > bg.pid; exec sleep ` + strconv.Itoa(bgSleepSeconds) + `' & )
		echo hi
		sleep ` + strconv.Itoa(bgSleepSeconds+10)}}

	const timeout = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	aliveAfterKill := make(chan bool, 1)
	go func() {
		time.Sleep(timeout + 2*time.Second)
		b, err := os.ReadFile(pidFile)
		if err != nil {
			aliveAfterKill <- false
			return
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		aliveAfterKill <- pid > 1 && pidAlive(pid)
	}()

	start := time.Now()
	res := commandutil.Run(ctx, cmd, wd, nil /*=statsListener*/, &interfaces.Stdio{})
	elapsed := time.Since(start)
	alive := <-aliveAfterKill
	t.Logf("elapsed=%s exitCode=%d err=%v stdout=%q orphanAlive2sAfterTimeout=%t", elapsed, res.ExitCode, res.Error, res.Stdout, alive)

	assert.True(t, status.IsDeadlineExceededError(res.Error), "expected DeadlineExceeded, got %v", res.Error)
	assert.Less(t, elapsed, timeout+5*time.Second, "Run should return shortly after the action timeout")
	assert.False(t, alive, "orphaned grandchild should be killed by the timeout kill")
}

// Orphaned grandchild that does NOT hold stdout (redirected to /dev/null).
// Run returns on time, but does the orphan survive the timeout kill?
func TestRun_OrphanedGrandchildNoStdout_Timeout(t *testing.T) {
	wd := testfs.MakeTempDir(t)
	pidFile := filepath.Join(wd, "bg.pid")
	killOnCleanup(t, pidFile)
	cmd := &repb.Command{Arguments: []string{"sh", "-c",
		`( sh -c 'echo $$ > bg.pid; exec sleep ` + strconv.Itoa(bgSleepSeconds) + `' >/dev/null 2>&1 & )
		echo hi
		sleep ` + strconv.Itoa(bgSleepSeconds+10)}}

	const timeout = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	res := commandutil.Run(ctx, cmd, wd, nil /*=statsListener*/, &interfaces.Stdio{})
	elapsed := time.Since(start)
	pid := readPid(t, pidFile)
	time.Sleep(200 * time.Millisecond)
	alive := pidAlive(pid)
	t.Logf("elapsed=%s exitCode=%d err=%v orphanPid=%d orphanAliveAfterRun=%t", elapsed, res.ExitCode, res.Error, pid, alive)

	assert.True(t, status.IsDeadlineExceededError(res.Error), "expected DeadlineExceeded, got %v", res.Error)
	assert.Less(t, elapsed, timeout+5*time.Second)
	assert.False(t, alive, "orphaned grandchild should be killed by the timeout kill")
}

// Exercise output larger than the kernel pipe buffer immediately before exit.
func TestRun_MainLargeOutputWithBackgroundPipe(t *testing.T) {
	wd := testfs.MakeTempDir(t)
	killOnCleanup(t, filepath.Join(wd, "bg.pid"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res := commandutil.Run(ctx, &repb.Command{Arguments: []string{"sh", "-c",
		"sh -c 'echo $$ > bg.pid; exec sleep 3' & chunk=" + strings.Repeat("x", 4096) + "; i=0; while [ \"$i\" -lt 1024 ]; do printf '%s' \"$chunk\"; i=$((i+1)); done"}},
		wd, nil, &interfaces.Stdio{})
	require.NoError(t, res.Error)
	require.Equal(t, 0, res.ExitCode)
	require.Equal(t, 4*1024*1024, len(res.Stdout))
	require.Equal(t, bytes.Repeat([]byte("x"), 4*1024*1024), res.Stdout)
}

// A writer can be slower than WaitDelay (for example an io.Pipe consumer).
// Synchronize the first Write with the shell so the remaining output is in
// the OS pipe when the main process exits, rather than relying on scheduling.
type exitSynchronizedWriter struct {
	dir   string
	first bool
	buf   bytes.Buffer
}

func (w *exitSynchronizedWriter) Write(p []byte) (int, error) {
	if !w.first {
		w.first = true
		if err := os.WriteFile(filepath.Join(w.dir, "reader-ready"), nil, 0600); err != nil {
			return 0, err
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(w.dir, "root-exiting")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return 0, os.ErrDeadlineExceeded
			}
			time.Sleep(time.Millisecond)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return w.buf.Write(p)
}

func TestRun_MainOutputSlowConsumer(t *testing.T) {
	wd := testfs.MakeTempDir(t)
	writer := &exitSynchronizedWriter{dir: wd}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res := commandutil.Run(ctx, &repb.Command{Arguments: []string{"sh", "-c",
		"printf a; while [ ! -f reader-ready ]; do sleep 0.01; done; printf '%s' " + strings.Repeat("x", 4096) + "; : > root-exiting"}},
		wd, nil, &interfaces.Stdio{Stdout: writer})
	require.NoError(t, res.Error)
	require.Equal(t, 0, res.ExitCode)
	t.Logf("captured=%d expected=4097 exitCode=%d", writer.buf.Len(), res.ExitCode)
	require.Equal(t, 4097, writer.buf.Len(), "all output written by main process must be captured")
}
