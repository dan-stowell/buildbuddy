package dirtools_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/cache/dirtools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/util/hash"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	rspb "github.com/buildbuddy-io/buildbuddy/proto/resource"
)

func setBlob(t *testing.T, env *testenv.TestEnv, ctx context.Context, b []byte) *repb.Digest {
	d := &repb.Digest{Hash: hash.Bytes(b), SizeBytes: int64(len(b))}
	rn := &rspb.ResourceName{Digest: d, CacheType: rspb.CacheType_CAS}
	require.NoError(t, env.GetCache().Set(ctx, rn, b))
	return d
}

// An action that only changes an input's permission bits (e.g. `chmod -x`)
// must not change the mode of the same digest in later workspaces. Metadata
// lives on the inode, so it leaks just like content when inodes are shared.
func TestVerifyV2_InputChmodDoesNotLeak(t *testing.T) {
	env, ctx := testEnv(t)
	d := setBlob(t, env, ctx, []byte("#!/bin/sh\necho hi\n"))
	tree := &repb.Tree{Root: &repb.Directory{Files: []*repb.FileNode{{Name: "tool.sh", Digest: d, IsExecutable: true}}}}

	ws1 := testfs.MakeTempDir(t)
	_, err := dirtools.DownloadTree(ctx, env, "", repb.DigestFunction_SHA256, tree, &dirtools.DownloadTreeOpts{RootDir: ws1})
	require.NoError(t, err)
	require.NoError(t, os.Chmod(filepath.Join(ws1, "tool.sh"), 0o600))

	ws2 := testfs.MakeTempDir(t)
	_, err = dirtools.DownloadTree(ctx, env, "", repb.DigestFunction_SHA256, tree, &dirtools.DownloadTreeOpts{RootDir: ws2})
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(ws2, "tool.sh"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "executable input lost its mode after another action chmod-ed it")
}

// Large inputs are fetched through ByteStream (and deduplicated across
// concurrent fetchers). Each concurrent action rewrites its copy in place
// (no truncation, no append) right after its download finishes; no other
// action, and no later action, may observe those writes.
func TestVerifyV2_ConcurrentLargeInputInPlaceWrites(t *testing.T) {
	for _, exec := range []bool{false, true} {
		t.Run(fmt.Sprintf("executable=%t", exec), func(t *testing.T) {
			env, ctx := testEnv(t)
			want := make([]byte, 5<<20) // larger than the batch read limit
			rand.New(rand.NewSource(7)).Read(want)
			d := setBlob(t, env, ctx, want)
			tree := &repb.Tree{Root: &repb.Directory{Files: []*repb.FileNode{
				{Name: "a.bin", Digest: d, IsExecutable: exec},
				{Name: "b.bin", Digest: d, IsExecutable: exec},
			}}}

			const actions = 6
			workspaces := make([]string, actions)
			var eg errgroup.Group
			for i := range actions {
				ws := testfs.MakeTempDir(t)
				workspaces[i] = ws
				eg.Go(func() error {
					if _, err := dirtools.DownloadTree(ctx, env, "", repb.DigestFunction_SHA256, tree, &dirtools.DownloadTreeOpts{RootDir: ws}); err != nil {
						return err
					}
					// Overwrite the first bytes of a.bin in place.
					f, err := os.OpenFile(filepath.Join(ws, "a.bin"), os.O_WRONLY, 0)
					if err != nil {
						return err
					}
					if _, err := f.WriteAt([]byte(fmt.Sprintf("CORRUPTED-BY-ACTION-%d", i)), 0); err != nil {
						f.Close()
						return err
					}
					return f.Close()
				})
			}
			require.NoError(t, eg.Wait())
			for i, ws := range workspaces {
				got, err := os.ReadFile(filepath.Join(ws, "b.bin"))
				require.NoError(t, err)
				require.True(t, bytes.Equal(want, got), "action %d: b.bin observed another path's in-place write (prefix %q)", i, got[:24])
			}

			later := testfs.MakeTempDir(t)
			_, err := dirtools.DownloadTree(ctx, env, "", repb.DigestFunction_SHA256, tree, &dirtools.DownloadTreeOpts{RootDir: later})
			require.NoError(t, err)
			for _, name := range []string{"a.bin", "b.bin"} {
				got, err := os.ReadFile(filepath.Join(later, name))
				require.NoError(t, err)
				require.True(t, bytes.Equal(want, got), "later action: %s has corrupted bytes (prefix %q)", name, got[:24])
			}
		})
	}
}

// TestVerifyV2_WarmMaterializationBench measures DownloadTree into a fresh
// workspace when every input is already in the file cache (no reflinks).
// Opt-in: run with --test_env=VERIFY_V2_BENCH=1.
func TestVerifyV2_WarmMaterializationBench(t *testing.T) {
	if os.Getenv("VERIFY_V2_BENCH") == "" {
		t.Skip("set VERIFY_V2_BENCH=1 to run")
	}
	if m, err := os.ReadFile("/proc/self/mounts"); err == nil {
		t.Logf("VBENCH mounts:\n%s", m)
	}
	// Optionally place the file cache and workspaces on another filesystem
	// (e.g. tmpfs, where copy_file_range cannot share extents).
	if dir := os.Getenv("VERIFY_V2_BENCH_DIR"); dir != "" {
		base, err := os.MkdirTemp(dir, "vbench-*")
		require.NoError(t, err)
		t.Cleanup(func() { os.RemoveAll(base) })
		t.Setenv("TMPDIR", base)
		t.Setenv("TEST_TMPDIR", base)
	}
	for _, tc := range []struct {
		name string
		n    int
		size func(i int) int
	}{
		{"small_10k", 10000, func(i int) int { return 64 + (i*7919)%8192 }},
		{"large_3x100MiB", 3, func(i int) int { return 100 << 20 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, ctx := testEnv(t)
			fc := env.GetFileCache()
			var sfs syscall.Statfs_t
			_ = syscall.Statfs(fc.TempDir(), &sfs)
			t.Logf("VBENCH filecache_tmp=%s fstype=0x%x", fc.TempDir(), sfs.Type)
			rng := rand.New(rand.NewSource(42))
			src := testfs.MakeTempDir(t)
			dirs := map[string]*repb.Directory{}
			var logical int64
			for i := range tc.n {
				b := make([]byte, tc.size(i))
				rng.Read(b)
				logical += int64(len(b))
				d := &repb.Digest{Hash: hash.Bytes(b), SizeBytes: int64(len(b))}
				p := filepath.Join(src, fmt.Sprintf("f%05d", i))
				require.NoError(t, os.WriteFile(p, b, 0o644))
				node := &repb.FileNode{Name: fmt.Sprintf("f%05d", i), Digest: d}
				require.NoError(t, fc.AddFile(ctx, node, p))
				require.NoError(t, os.Remove(p))
				dn := fmt.Sprintf("d%03d", i%100)
				if dirs[dn] == nil {
					dirs[dn] = &repb.Directory{}
				}
				dirs[dn].Files = append(dirs[dn].Files, node)
			}
			tree := &repb.Tree{Root: &repb.Directory{}}
			for i := range 100 {
				dn := fmt.Sprintf("d%03d", i)
				dir, ok := dirs[dn]
				if !ok {
					continue
				}
				dd, err := digest.ComputeForMessage(dir, repb.DigestFunction_SHA256)
				require.NoError(t, err)
				tree.Root.Directories = append(tree.Root.Directories, &repb.DirectoryNode{Name: dn, Digest: dd})
				tree.Children = append(tree.Children, dir)
			}
			for round := range 5 {
				ws := testfs.MakeTempDir(t)
				start := time.Now()
				info, err := dirtools.DownloadTree(ctx, env, "", repb.DigestFunction_SHA256, tree, &dirtools.DownloadTreeOpts{RootDir: ws})
				require.NoError(t, err)
				el := time.Since(start)
				require.Equal(t, int64(0), info.FileCount, "expected all inputs to be file cache hits")
				t.Logf("VBENCH %s round=%d files=%d logical_bytes=%d elapsed=%s", tc.name, round, tc.n, logical, el)
				require.NoError(t, os.RemoveAll(ws))
			}
			// Proxy for a filesystem without extent sharing: copy every cached
			// file with plain read/write (wrapping defeats copy_file_range).
			for round := range 3 {
				ws := testfs.MakeTempDir(t)
				start := time.Now()
				i := 0
				for _, dir := range tree.Children {
					for _, node := range dir.Files {
						src, err := fc.Open(ctx, node)
						require.NoError(t, err)
						dst, err := os.OpenFile(filepath.Join(ws, fmt.Sprintf("p%05d", i)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
						require.NoError(t, err)
						_, err = io.Copy(struct{ io.Writer }{dst}, struct{ io.Reader }{src})
						require.NoError(t, err)
						require.NoError(t, dst.Close())
						require.NoError(t, src.Close())
						i++
					}
				}
				t.Logf("VBENCH %s userspace_copy_proxy round=%d files=%d elapsed=%s", tc.name, round, i, time.Since(start))
				require.NoError(t, os.RemoveAll(ws))
			}
		})
	}
}
