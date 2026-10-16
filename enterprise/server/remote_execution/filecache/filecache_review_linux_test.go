//go:build linux

package filecache_test

import (
	"context"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/filecache"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/util/claims"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestReviewSparseCrossDeviceAccounting(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux tmpfs")
	}
	flags.Set(t, "executor.enable_fastcopy_reflinking", false)
	src, err := os.CreateTemp("/dev/shm", "filecache-review-*")
	if err != nil {
		t.Skipf("tmpfs unavailable: %s", err)
	}
	defer os.Remove(src.Name())
	defer src.Close()
	require.NoError(t, src.Truncate(4<<20))
	_, err = src.WriteAt([]byte("x"), (4<<20)-1)
	require.NoError(t, err)
	root := testfs.MakeTempDir(t)
	si, err := src.Stat()
	require.NoError(t, err)
	ri, err := os.Stat(root)
	require.NoError(t, err)
	if si.Sys().(*syscall.Stat_t).Dev == ri.Sys().(*syscall.Stat_t).Dev {
		t.Skip("requires different filesystems")
	}
	_, err = src.Seek(0, 0)
	require.NoError(t, err)
	d, err := digest.Compute(src, repb.DigestFunction_SHA256)
	require.NoError(t, err)
	fc, err := filecache.NewFileCache(root, 1<<20, false)
	require.NoError(t, err)
	defer fc.Close()
	fc.WaitForDirectoryScanToComplete()
	node := &repb.FileNode{Digest: d}
	ctx := claims.AuthContextWithJWT(context.Background(), &claims.Claims{GroupID: "GR_REVIEW"}, nil)
	err = fc.AddFile(ctx, node, src.Name())
	if err != nil {
		t.Logf("oversized admission rejected: %s", err)
		return
	}
	var allocated int64
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			allocated += info.Sys().(*syscall.Stat_t).Blocks * 512
		}
		return nil
	}))
	t.Logf("source_allocated=%d cache_allocated=%d budget=%d", si.Sys().(*syscall.Stat_t).Blocks*512, allocated, 1<<20)
	require.LessOrEqual(t, allocated, int64(1<<20), "copied sparse file exceeds cache budget")
}
