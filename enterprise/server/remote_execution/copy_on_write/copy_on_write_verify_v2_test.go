package copy_on_write_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/copy_on_write"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/filecache"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// Snapshot chunks are linked read-only from the file cache into the COW data
// dir (sharing the inode). Guest writes through the COW store must land in a
// private dirty chunk and never modify the cached chunk bytes.
func TestVerifyV2_COWWriteDoesNotModifyFileCacheChunk(t *testing.T) {
	flags.Set(t, "executor.enable_local_snapshot_sharing", true)
	flags.Set(t, "executor.enable_remote_snapshot_sharing", false)

	ctx := t.Context()
	env := testenv.GetTestEnv(t)
	fc, err := filecache.NewFileCache(testfs.MakeTempDir(t), 1_000_000, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, fc.Close()) })
	fc.WaitForDirectoryScanToComplete()
	env.SetFileCache(fc)

	const chunkSizeBytes = int64(4096)
	orig := make([][]byte, 2)
	digests := make([]*repb.Digest, 2)
	for i := range digests {
		orig[i] = bytes.Repeat([]byte{byte('a' + i)}, int(chunkSizeBytes))
		d, err := digest.Compute(bytes.NewReader(orig[i]), repb.DigestFunction_BLAKE3)
		require.NoError(t, err)
		digests[i] = d
		path := filepath.Join(testfs.MakeTempDir(t), fmt.Sprintf("chunk-%d", i))
		require.NoError(t, os.WriteFile(path, orig[i], 0644))
		require.NoError(t, fc.AddReadOnly(ctx, &repb.FileNode{Digest: d}, path))
	}

	newStore := func() *copy_on_write.COWStore {
		dataDir := testfs.MakeTempDir(t)
		lru, err := copy_on_write.GetSharedMmapLRU(dataDir)
		require.NoError(t, err)
		chunks := make([]*copy_on_write.Mmap, 0, len(digests))
		for i, d := range digests {
			chunk, err := copy_on_write.NewLazyMmap(ctx, env, dataDir, int64(i)*chunkSizeBytes, d, "", false, lru)
			require.NoError(t, err)
			chunks = append(chunks, chunk)
		}
		cow, err := copy_on_write.NewCOWStore(ctx, env, "test", chunks, copy_on_write.COWOptions{
			ChunkSizeBytes: chunkSizeBytes,
			TotalSizeBytes: int64(len(chunks)) * chunkSizeBytes,
			DataDir:        dataDir,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = cow.Close() })
		return cow
	}

	cow := newStore()
	// Read first so the clean chunk is linked from the cache and mmapped.
	buf := make([]byte, 16)
	_, err = cow.ReadAt(buf, 0)
	require.NoError(t, err)
	_, err = cow.WriteAt([]byte("GUEST-WRITE"), 100)
	require.NoError(t, err)
	_, err = cow.WriteAt([]byte("SPANNING-WRITE"), chunkSizeBytes-5)
	require.NoError(t, err)
	require.NoError(t, cow.Sync())

	for i, d := range digests {
		f, err := fc.Open(ctx, &repb.FileNode{Digest: d})
		require.NoError(t, err)
		got, err := io.ReadAll(f)
		require.NoError(t, f.Close())
		require.NoError(t, err)
		require.True(t, bytes.Equal(orig[i], got), "cached chunk %d modified by a COW write", i)
	}

	other := newStore()
	got := make([]byte, 2*chunkSizeBytes)
	_, err = other.ReadAt(got, 0)
	require.NoError(t, err)
	require.True(t, bytes.Equal(append(append([]byte{}, orig[0]...), orig[1]...), got), "second store saw first store's writes")
}
