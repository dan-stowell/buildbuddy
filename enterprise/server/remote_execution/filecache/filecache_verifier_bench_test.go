package filecache_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/filecache"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/util/claims"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// procIO returns (wchar, write_bytes) for this process, or -1 if unavailable.
func procIO() (int64, int64) {
	b, err := os.ReadFile("/proc/self/io")
	if err != nil {
		return -1, -1
	}
	wchar, wb := int64(-1), int64(-1)
	for line := range strings.SplitSeq(string(b), "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		switch k {
		case "wchar":
			wchar = n
		case "write_bytes":
			wb = n
		}
	}
	return wchar, wb
}

// uniqueBlockBytes sums allocated bytes of files under dir whose inode is not
// shared with any other path (nlink == 1), i.e. disk consumed only by dir.
func uniqueBlockBytes(t *testing.T, dir string) (unique, total int64) {
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		total += st.Blocks * 512
		if st.Nlink == 1 {
			unique += st.Blocks * 512
		}
		return nil
	})
	require.NoError(t, err)
	return
}

type benchFile struct {
	node *repb.FileNode
	path string
	size int64
}

func makeBenchFiles(t *testing.T, dir string, n int, size func(i int) int, seed int64) []benchFile {
	rng := rand.New(rand.NewSource(seed))
	out := make([]benchFile, n)
	for i := range out {
		b := make([]byte, size(i))
		rng.Read(b)
		sum := sha256.Sum256(b)
		p := filepath.Join(dir, fmt.Sprintf("f%05d", i))
		require.NoError(t, os.WriteFile(p, b, 0644))
		out[i] = benchFile{
			node: &repb.FileNode{Digest: &repb.Digest{Hash: hex.EncodeToString(sum[:]), SizeBytes: int64(len(b))}},
			path: p,
			size: int64(len(b)),
		}
	}
	return out
}

// TestVerifierMaterializationBench measures the cost of admitting a tree into
// the filecache (AddFile, as after an input download or output upload) and of
// materializing it into fresh workspaces (FastLinkFile), without reflinks.
// It only uses APIs that exist before and after the isolation change, so the
// same test can be run on both versions.
func TestVerifierMaterializationBench(t *testing.T) {
	runVerifierMaterializationBench(t, false)
}

func TestVerifierReadOnlyBench(t *testing.T) {
	runVerifierMaterializationBench(t, true)
}

func runVerifierMaterializationBench(t *testing.T, readOnly bool) {
	flags.Set(t, "executor.enable_fastcopy_reflinking", false)
	ctx := claims.AuthContextWithJWT(context.Background(), &claims.Claims{GroupID: "GR0000000003"}, nil)
	var sfs syscall.Statfs_t
	_ = syscall.Statfs(os.TempDir(), &sfs)
	t.Logf("BENCH env tmp=%s fstype=0x%x seed=42 readonly=%t", os.TempDir(), sfs.Type, readOnly)

	for _, tc := range []struct {
		name string
		n    int
		size func(i int) int
	}{
		{"small_10k", 10000, func(i int) int { return 64 + (i*7919)%8192 }},
		{"large_3x100MiB", 3, func(i int) int { return 100 << 20 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc, err := filecache.NewFileCache(testfs.MakeTempDir(t), 50_000_000_000, false)
			require.NoError(t, err)
			fc.WaitForDirectoryScanToComplete()
			files := makeBenchFiles(t, testfs.MakeTempDir(t), tc.n, tc.size, 42)
			var logical int64
			for _, f := range files {
				logical += f.size
			}

			w0, wb0 := procIO()
			start := time.Now()
			for _, f := range files {
				if readOnly {
					require.NoError(t, fc.AddReadOnly(ctx, f.node, f.path))
				} else {
					require.NoError(t, fc.AddFile(ctx, f.node, f.path))
				}
			}
			el := time.Since(start)
			w1, wb1 := procIO()
			t.Logf("BENCH %s op=AddFile files=%d logical_bytes=%d elapsed=%s wchar=%d write_bytes=%d", tc.name, tc.n, logical, el, w1-w0, wb1-wb0)

			start = time.Now()
			for _, f := range files {
				require.NoError(t, fc.AddFile(ctx, f.node, f.path))
			}
			t.Logf("BENCH %s op=DuplicateAdd elapsed=%s", tc.name, time.Since(start))
			for round := range 3 {
				ws := testfs.MakeTempDir(t)
				w0, wb0 := procIO()
				start := time.Now()
				for i, f := range files {
					link := fc.MaterializeWritable
					if readOnly {
						link = fc.LinkReadOnly
					}
					if !link(ctx, f.node, filepath.Join(ws, fmt.Sprintf("o%05d", i))) {
						t.Fatalf("miss for file %d", i)
					}
				}
				el := time.Since(start)
				w1, wb1 := procIO()
				unique, total := uniqueBlockBytes(t, ws)
				t.Logf("BENCH %s op=FastLinkFile round=%d files=%d logical_bytes=%d elapsed=%s wchar=%d write_bytes=%d ws_alloc_bytes=%d ws_unshared_alloc_bytes=%d", tc.name, round, tc.n, logical, el, w1-w0, wb1-wb0, total, unique)
				require.NoError(t, os.RemoveAll(ws))
			}
		})
	}
}
