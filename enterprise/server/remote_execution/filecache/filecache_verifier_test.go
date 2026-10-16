package filecache_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/filecache"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/util/claims"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

func verifierNode(t *testing.T, b []byte) *repb.FileNode {
	d, err := digest.Compute(bytes.NewReader(b), repb.DigestFunction_SHA256)
	require.NoError(t, err)
	return &repb.FileNode{Digest: d}
}

// An entry admitted through the Writer API, materialized into a workspace and
// then overwritten in place (O_TRUNC + write, as a tool rewriting its input
// would do) must still be served intact by Read and by later FastLinkFile
// calls.
func TestVerifierWriterEntrySurvivesInPlaceRewrite(t *testing.T) {
	flags.Set(t, "executor.enable_fastcopy_reflinking", false)
	ctx := claims.AuthContextWithJWT(context.Background(), &claims.Claims{GroupID: "GR0000000001"}, nil)
	fc, err := filecache.NewFileCache(testfs.MakeTempDir(t), 1_000_000_000, false)
	require.NoError(t, err)
	fc.WaitForDirectoryScanToComplete()

	original := bytes.Repeat([]byte("abcdefgh"), 4096)
	node := verifierNode(t, original)
	w, err := fc.Writer(ctx, node, repb.DigestFunction_SHA256)
	require.NoError(t, err)
	_, err = w.Write(original)
	require.NoError(t, err)
	require.NoError(t, w.Commit())
	require.NoError(t, w.Close())

	ws1 := filepath.Join(testfs.MakeTempDir(t), "in")
	require.True(t, fc.FastLinkFile(ctx, node, ws1))
	require.NoError(t, os.WriteFile(ws1, []byte("rewritten by action 1"), 0644))

	got, err := fc.Read(ctx, node)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(original), sha256.Sum256(got), "filecache Read returned mutated bytes")

	ws2 := filepath.Join(testfs.MakeTempDir(t), "in")
	require.True(t, fc.FastLinkFile(ctx, node, ws2))
	got2, err := os.ReadFile(ws2)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(original), sha256.Sum256(got2), "later action received mutated bytes")
}

// Readers racing with repeated admissions of the same key must either miss or
// see the complete file, never a partially copied one.
func TestVerifierConcurrentAddAndLinkNeverPartial(t *testing.T) {
	flags.Set(t, "executor.enable_fastcopy_reflinking", false)
	ctx := claims.AuthContextWithJWT(context.Background(), &claims.Claims{GroupID: "GR0000000002"}, nil)
	fc, err := filecache.NewFileCache(testfs.MakeTempDir(t), 1_000_000_000, false)
	require.NoError(t, err)
	fc.WaitForDirectoryScanToComplete()

	data := make([]byte, 8<<20)
	for i := range data {
		data[i] = byte(i*7 + i/4096)
	}
	node := verifierNode(t, data)
	srcDir := testfs.MakeTempDir(t)
	want := sha256.Sum256(data)

	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 40 {
			src := filepath.Join(srcDir, fmt.Sprintf("src-%d", i))
			if err := os.WriteFile(src, data, 0644); err != nil {
				t.Error(err)
				return
			}
			if err := fc.AddFile(ctx, node, src); err != nil {
				t.Error(err)
				return
			}
			os.Remove(src)
		}
		stop.Store(true)
	})
	var hits, misses atomic.Int64
	for r := range 4 {
		wg.Go(func() {
			dir := testfs.MakeTempDir(t)
			for i := 0; !stop.Load(); i++ {
				p := filepath.Join(dir, fmt.Sprintf("out-%d", i))
				if !fc.FastLinkFile(ctx, node, p) {
					misses.Add(1)
					continue
				}
				b, err := os.ReadFile(p)
				if err != nil {
					t.Error(err)
					return
				}
				if sha256.Sum256(b) != want {
					t.Errorf("reader %d saw partial/incorrect file: %d bytes", r, len(b))
					return
				}
				hits.Add(1)
				os.Remove(p)
			}
		})
	}
	wg.Wait()
	t.Logf("hits=%d misses=%d", hits.Load(), misses.Load())
	require.Greater(t, hits.Load(), int64(0))
}

// Immutable consumers share storage; workspace consumers must remain isolated.
func TestExplicitTransferModes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux inode transfer semantics")
	}
	flags.Set(t, "executor.enable_fastcopy_reflinking", false)
	ctx := context.Background()
	fc, err := filecache.NewFileCache(testfs.MakeTempDir(t), 1_000_000, false)
	require.NoError(t, err)
	t.Cleanup(func() { fc.Close() })
	fc.WaitForDirectoryScanToComplete()
	data := []byte("immutable contents")
	node := verifierNode(t, data)
	dir := testfs.MakeTempDir(t)
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, data, 0644))
	require.NoError(t, fc.AddReadOnly(ctx, node, src))
	ro := filepath.Join(dir, "ro")
	require.True(t, fc.LinkReadOnly(ctx, node, ro))
	si, err := os.Stat(src)
	require.NoError(t, err)
	ri, err := os.Stat(ro)
	require.NoError(t, err)
	require.True(t, os.SameFile(si, ri))
	ws := filepath.Join(dir, "workspace")
	require.True(t, fc.MaterializeWritable(ctx, node, ws))
	wi, err := os.Stat(ws)
	require.NoError(t, err)
	require.False(t, os.SameFile(si, wi))
	require.NoError(t, os.WriteFile(ws, []byte("mutated"), 0644))
	got, err := fc.Read(ctx, node)
	require.NoError(t, err)
	require.Equal(t, data, got)
	// A hit must succeed even if the source no longer exists (no stat/copy).
	require.NoError(t, fc.AddFile(ctx, node, filepath.Join(dir, "missing")))
}

func TestWriterCommitRenamesPrivateTemp(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux inode transfer semantics")
	}
	fc, err := filecache.NewFileCache(testfs.MakeTempDir(t), 1_000_000, false)
	require.NoError(t, err)
	t.Cleanup(func() { fc.Close() })
	fc.WaitForDirectoryScanToComplete()
	data := []byte("private writer bytes")
	node := verifierNode(t, data)
	ctx := context.Background()
	w, err := fc.Writer(ctx, node, repb.DigestFunction_SHA256)
	require.NoError(t, err)
	_, err = w.Write(data)
	require.NoError(t, err)
	files, err := os.ReadDir(fc.TempDir())
	require.NoError(t, err)
	require.Len(t, files, 1)
	before, err := os.Stat(filepath.Join(fc.TempDir(), files[0].Name()))
	require.NoError(t, err)
	require.NoError(t, w.Commit())
	cached, err := fc.Open(ctx, node)
	require.NoError(t, err)
	defer cached.Close()
	after, err := cached.Stat()
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "commit must rename, not copy")
}

func TestTrustedWorkloadHardlinkOptOut(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux inode transfer semantics")
	}
	flags.Set(t, "executor.local_cache_trusted_workspace_hardlinks", true)
	flags.Set(t, "executor.enable_fastcopy_reflinking", false)
	fc, err := filecache.NewFileCache(testfs.MakeTempDir(t), 1_000_000, false)
	require.NoError(t, err)
	t.Cleanup(func() { fc.Close() })
	fc.WaitForDirectoryScanToComplete()
	data := []byte("trusted immutable input")
	node := verifierNode(t, data)
	ctx := context.Background()
	_, err = fc.Write(ctx, node, data)
	require.NoError(t, err)
	cacheFile, err := fc.Open(ctx, node)
	require.NoError(t, err)
	defer cacheFile.Close()
	before, err := cacheFile.Stat()
	require.NoError(t, err)
	dir := testfs.MakeTempDir(t)
	ws := filepath.Join(dir, "ws")
	require.True(t, fc.MaterializeWritable(ctx, node, ws))
	after, err := os.Stat(ws)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	flags.Set(t, "executor.local_cache_always_clone", true)
	safe := filepath.Join(dir, "safe")
	require.True(t, fc.MaterializeWritable(ctx, node, safe))
	safeInfo, err := os.Stat(safe)
	require.NoError(t, err)
	require.False(t, os.SameFile(before, safeInfo))
}
