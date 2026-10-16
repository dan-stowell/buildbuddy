package filecache_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
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
