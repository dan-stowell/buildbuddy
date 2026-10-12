package ociconv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testregistry"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/ext4"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	ctr "github.com/google/go-containerregistry/pkg/v1"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type manifestImage struct {
	ctr.Image
	manifest *ctr.Manifest
	err      error
}

func (i manifestImage) Manifest() (*ctr.Manifest, error) { return i.manifest, i.err }

func TestFitsTmpfs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sizes []int64
		limit int64
		err   error
		want  bool
	}{
		{name: "manifest error", limit: 10, err: errors.New("manifest unavailable")},
		{name: "disabled", limit: 0},
		{name: "negative limit", limit: -1},
		{name: "empty manifest", limit: 10, want: true},
		{name: "negative layer", sizes: []int64{-1}, limit: 10},
		{name: "single oversize layer", sizes: []int64{11}, limit: 10},
		{name: "multiple layers below limit", sizes: []int64{3, 4, 2}, limit: 10, want: true},
		{name: "multiple layers exact limit", sizes: []int64{3, 4, 3}, limit: 10, want: true},
		{name: "multiple layers exceed limit", sizes: []int64{3, 4, 4}, limit: 10},
		{name: "zero layer at exact limit", sizes: []int64{10, 0}, limit: 10, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &ctr.Manifest{}
			for _, size := range tc.sizes {
				m.Layers = append(m.Layers, ctr.Descriptor{Size: size})
			}
			require.Equal(t, tc.want, fitsTmpfs(manifestImage{manifest: m, err: tc.err}, tc.limit))
		})
	}
}

func requireMountPrivileges(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root and CAP_SYS_ADMIN for tmpfs mounting")
	}
	dir := t.TempDir()
	// Root in a restricted container may still lack mount privileges.
	if err := unix.Mount("tmpfs", dir, "tmpfs", 0, "size=1048576"); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			t.Skipf("tmpfs mounting unavailable: %s", err)
		}
		require.NoError(t, err)
	}
	require.NoError(t, unix.Unmount(dir, 0))
}

// Record each extraction attempt, then exec the real tar so the recorded PID
// is the process CommandContext must kill. Successful attempts wait for the
// filesystem assertion before extracting. The checkpoint sleep lets the test
// cancel deterministically while tar has a partially written file.
func observeTar(t *testing.T, pause bool) string {
	t.Helper()
	realTar, err := exec.LookPath("tar")
	require.NoError(t, err)
	realTar, err = filepath.Abs(realTar)
	require.NoError(t, err)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "attempts")
	t.Setenv("UNPACK_TEST_LOG", logPath)
	t.Setenv("UNPACK_TEST_TAR", realTar)
	extra := ""
	gate := `while [ ! -e "$UNPACK_TEST_LOG.release.$$" ]; do sleep 0.01; done`
	if pause {
		extra = "--checkpoint=2 --checkpoint-action=sleep=60"
		gate = ""
	}
	script := fmt.Sprintf(`#!/bin/sh
# Production supplies --extract --directory PATH as its first arguments.
printf '%%s %%s\n' "$$" "$3" >> "$UNPACK_TEST_LOG"
%s
exec "$UNPACK_TEST_TAR" %s "$@"
`, gate, extra)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tar"), []byte(script), 0755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return logPath
}

func unpackAttempt(t *testing.T, logPath string, index int) (int, string) {
	t.Helper()
	var lines []string
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(logPath)
		if err != nil {
			return false
		}
		lines = strings.Fields(string(b))
		return len(lines) >= 2*(index+1)
	}, 10*time.Second, 10*time.Millisecond)
	pid, err := strconv.Atoi(lines[2*index])
	require.NoError(t, err)
	return pid, lines[2*index+1]
}

func assertUnpackFilesystem(t *testing.T, dir string, tmpfs bool) {
	t.Helper()
	var st unix.Statfs_t
	require.NoError(t, unix.Statfs(dir, &st))
	if tmpfs {
		require.EqualValues(t, unix.TMPFS_MAGIC, st.Type, "unpack directory must be mounted on tmpfs")
		return
	}
	var parent unix.Statfs_t
	require.NoError(t, unix.Statfs(filepath.Dir(dir), &parent))
	require.Equal(t, parent.Type, st.Type)
	require.Equal(t, parent.Fsid, st.Fsid, "disk extraction must use the workspace filesystem")
}

func assertUnpackCleaned(t *testing.T, root, dir string) {
	t.Helper()
	require.NoDirExists(t, dir)
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	require.NoError(t, err)
	require.NotContains(t, string(mounts), dir, "unpack mount must be removed")
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, strings.HasPrefix(e.Name(), "unpack-"))
	}
}

func TestTmpfsExtractionAndSizeFallback(t *testing.T) {
	requireMountPrivileges(t)
	reg := testregistry.Run(t, testregistry.Opts{})
	t.Cleanup(func() { require.NoError(t, reg.Shutdown()) })
	content := []byte(strings.Repeat("rootfs content\n", 150_000))
	ref, img := reg.PushNamedImageWithFiles(t, "observed-tmpfs:latest", map[string][]byte{"data": content}, nil)
	for _, tc := range []struct {
		name     string
		limit    int64
		attempts int
	}{
		{"tmpfs success", 64 << 20, 1},
		{"size exhaustion retries on disk", 1 << 20, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.True(t, fitsTmpfs(img, tc.limit), "fixture must pass the compressed-size filter")
			flags.Set(t, "executor.oci_unpack_tmpfs_bytes", tc.limit)
			flags.Set(t, "executor.prezeroed_ext4_images", true)
			logPath := observeTar(t, false)
			root := t.TempDir()
			image := filepath.Join(root, "output.ext4")
			done := make(chan error, 1)
			ctx := t.Context()
			go func() { done <- ConvertContainerToExt4FS(ctx, img, root, ref, image) }()
			pid, dir := unpackAttempt(t, logPath, 0)
			assertUnpackFilesystem(t, dir, true)
			require.NoError(t, os.WriteFile(fmt.Sprintf("%s.release.%d", logPath, pid), nil, 0644))
			if tc.attempts == 2 {
				retryPID, retryDir := unpackAttempt(t, logPath, 1)
				require.Equal(t, dir, retryDir)
				assertUnpackFilesystem(t, retryDir, false)
				require.NoError(t, os.WriteFile(fmt.Sprintf("%s.release.%d", logPath, retryPID), nil, 0644))
			}
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(20 * time.Second):
				t.Fatal("conversion did not finish")
			}
			b, err := os.ReadFile(logPath)
			require.NoError(t, err)
			require.Len(t, strings.Fields(string(b)), 2*tc.attempts)
			assertUnpackCleaned(t, root, dir)
			out := t.TempDir()
			require.NoError(t, ext4.ImageToDirectory(context.Background(), image, out, []string{"/data"}))
			actual, err := os.ReadFile(filepath.Join(out, "data"))
			require.NoError(t, err)
			require.Equal(t, content, actual)
		})
	}
}

func TestCancellationAfterPartialExtraction(t *testing.T) {
	for _, tmpfs := range []bool{false, true} {
		t.Run(fmt.Sprintf("tmpfs=%t", tmpfs), func(t *testing.T) {
			if tmpfs {
				requireMountPrivileges(t)
			}
			limit := int64(0)
			if tmpfs {
				limit = 64 << 20
			}
			flags.Set(t, "executor.oci_unpack_tmpfs_bytes", limit)
			reg := testregistry.Run(t, testregistry.Opts{})
			t.Cleanup(func() { require.NoError(t, reg.Shutdown()) })
			content := []byte(strings.Repeat("partial extraction\n", 150_000))
			ref, img := reg.PushNamedImageWithFiles(t, "cancel-partial:latest", map[string][]byte{"data": content}, nil)
			logPath := observeTar(t, true)
			root := t.TempDir()
			image := filepath.Join(root, "output.ext4")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- ConvertContainerToExt4FS(ctx, img, root, ref, image) }()
			pid, dir := unpackAttempt(t, logPath, 0)
			require.Eventually(t, func() bool {
				st, err := os.Stat(filepath.Join(dir, "data"))
				return err == nil && st.Size() > 0 && st.Size() < int64(len(content))
			}, 10*time.Second, 10*time.Millisecond, "tar must have written only part of the file")
			assertUnpackFilesystem(t, dir, tmpfs)
			require.NoError(t, unix.Kill(pid, 0), "tar must still be running before cancellation")
			cancel()
			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("extraction did not stop after cancellation")
			}
			require.ErrorIs(t, unix.Kill(pid, 0), unix.ESRCH, "tar must have exited and been reaped")
			assertUnpackCleaned(t, root, dir)
			require.NoFileExists(t, image)
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			require.Empty(t, entries)
			b, err := os.ReadFile(logPath)
			require.NoError(t, err)
			require.Len(t, strings.Fields(string(b)), 2, "cancellation must not retry extraction")
		})
	}
}
