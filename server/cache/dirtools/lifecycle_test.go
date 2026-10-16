package dirtools_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	rspb "github.com/buildbuddy-io/buildbuddy/proto/resource"
	"github.com/buildbuddy-io/buildbuddy/server/cache/dirtools"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/cachetools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testdigest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
)

// lifecycleCache injects failures at the real CAS's storage boundary, after
// the RPC has started. The server and transports remain alive across tasks.
type lifecycleCache struct {
	interfaces.Cache
	mu         sync.Mutex
	hook       func(context.Context, string) error
	cancelRead context.CancelFunc
}

func (c *lifecycleCache) setHook(h func(context.Context, string) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hook = h
	c.cancelRead = nil
}
func (c *lifecycleCache) before(ctx context.Context, op string) error {
	c.mu.Lock()
	h := c.hook
	c.mu.Unlock()
	if h != nil {
		return h(ctx, op)
	}
	return nil
}
func (c *lifecycleCache) Reader(ctx context.Context, r *rspb.ResourceName, off, limit int64) (io.ReadCloser, error) {
	if err := c.before(ctx, "read"); err != nil {
		return nil, err
	}
	reader, err := c.Cache.Reader(ctx, r, off, limit)
	c.mu.Lock()
	cancel := c.cancelRead
	c.mu.Unlock()
	if err == nil && cancel != nil {
		return &cancelOnRead{ReadCloser: reader, cancel: cancel}, nil
	}
	return reader, err
}
func (c *lifecycleCache) GetMulti(ctx context.Context, rs []*rspb.ResourceName) (map[*repb.Digest][]byte, error) {
	if err := c.before(ctx, "batch"); err != nil {
		return nil, err
	}
	return c.Cache.GetMulti(ctx, rs)
}
func (c *lifecycleCache) Writer(ctx context.Context, r *rspb.ResourceName) (interfaces.CommittedWriteCloser, error) {
	if err := c.before(ctx, "write"); err != nil {
		return nil, err
	}
	return c.Cache.Writer(ctx, r)
}

// Cancel only after the CAS has read real bytes from storage.
type cancelOnRead struct {
	io.ReadCloser
	once   sync.Once
	cancel context.CancelFunc
}

func (r *cancelOnRead) Read(b []byte) (int, error) {
	n, err := r.ReadCloser.Read(b)
	if n > 0 {
		r.once.Do(r.cancel)
	}
	return n, err
}

func TestTaskTransferLifecycle(t *testing.T) {
	for _, mode := range []string{"batch", "cache_hit", "stream", "filecache", "missing", "disk_error", "cancel_batch", "cancel_stream", "upload", "cancel_upload", "write_error", "tree_upload", "cancel_tree_upload", "tree_upload_write_error", "cancel_partial_stream", "cancel_partial_filecache", "chunked", "cancel_chunked"} {
		for _, n := range []int{10, 50} {
			t.Run(fmt.Sprintf("%s/N%d", mode, n), func(t *testing.T) {
				isUpload := strings.Contains(mode, "upload") || strings.Contains(mode, "write_error")
				isCancel := strings.Contains(mode, "cancel")
				isWriteError := strings.Contains(mode, "write_error")
				testleak.Check(t)
				testleak.CheckFDs(t)
				flags.Set(t, "grpc_server_worker_multiplier", 0)
				flags.Set(t, "cache.client.input_download_max_batch_files", 8)
				var c *lifecycleCache
				env, ctx := testEnv(t, func(base interfaces.Cache) interfaces.Cache {
					c = &lifecycleCache{Cache: base}
					return c
				})
				fc := env.GetFileCache()
				t.Cleanup(func() { require.NoError(t, fc.(io.Closer).Close()) })
				size := 1024
				if mode == "stream" || mode == "filecache" || mode == "cancel_stream" || strings.Contains(mode, "partial") || isUpload {
					size = int(dirtools.BatchReadLimitBytes) + 1
				}
				if strings.Contains(mode, "chunked") {
					size = 6 * 1024 * 1024
				}
				rn, data := testdigest.RandomCASResourceBuf(t, int64(size))
				require.NoError(t, c.Set(ctx, rn, data))
				node := &repb.FileNode{Name: "input", Digest: rn.GetDigest()}
				if strings.Contains(mode, "chunked") {
					var chunks []*repb.Digest
					for off := 0; off < len(data); off += 2 * 1024 * 1024 {
						b := data[off : off+2*1024*1024]
						d, err := digest.Compute(bytes.NewReader(b), repb.DigestFunction_SHA256)
						require.NoError(t, err)
						require.NoError(t, c.Set(ctx, digest.NewCASResourceName(d, "", repb.DigestFunction_SHA256).ToProto(), b))
						chunks = append(chunks, d)
					}
					_, err := env.GetContentAddressableStorageClient().SpliceBlob(ctx, &repb.SpliceBlobRequest{BlobDigest: node.Digest, ChunkDigests: chunks, DigestFunction: repb.DigestFunction_SHA256})
					require.NoError(t, err)
					split, err := env.GetContentAddressableStorageClient().SplitBlob(ctx, &repb.SplitBlobRequest{BlobDigest: node.Digest, DigestFunction: repb.DigestFunction_SHA256})
					require.NoError(t, err)
					require.Len(t, split.GetChunkDigests(), 3)
				}
				tree := &repb.Tree{Root: &repb.Directory{Files: []*repb.FileNode{node}}}
				if mode == "batch" || mode == "cancel_batch" {
					// Distinct digests force several parallel batches rather than deduping.
					for j := range 63 {
						r, b := testdigest.RandomCASResourceBuf(t, 1024)
						require.NoError(t, c.Set(ctx, r, b))
						tree.Root.Files = append(tree.Root.Files, &repb.FileNode{Name: fmt.Sprintf("input%d", j), Digest: r.GetDigest()})
					}
				}
				root := testfs.MakeTempDir(t)
				// Pre-warm the real RPC transport and compression pools before measuring.
				for range 3 {
					require.NoError(t, cachetools.GetBlob(ctx, env.GetByteStreamClient(), digest.NewCASResourceName(node.Digest, "", repb.DigestFunction_SHA256), io.Discard))
				}
				if mode == "missing" {
					require.NoError(t, c.Delete(ctx, rn))
				}
				op := func(i int) {
					c.setHook(nil)
					callCtx, cancel := context.WithCancel(ctx)
					defer cancel()
					if mode != "cache_hit" {
						for _, f := range tree.Root.Files {
							fc.DeleteFile(ctx, f)
						}
					}
					require.NoError(t, os.RemoveAll(root))
					require.NoError(t, os.MkdirAll(root, 0755))
					if mode == "disk_error" {
						require.NoError(t, os.Mkdir(filepath.Join(root, "input"), 0755))
					}
					if isCancel && !strings.Contains(mode, "partial") && mode != "cancel_chunked" {
						wanted := "read"
						if mode == "cancel_batch" {
							wanted = "batch"
						}
						if isUpload {
							wanted = "write"
						}
						c.setHook(func(serverCtx context.Context, kind string) error {
							if kind != wanted {
								return nil
							}
							cancel()
							<-serverCtx.Done()
							return status.FromContextError(serverCtx)
						})
					}
					if strings.Contains(mode, "partial") || mode == "cancel_chunked" {
						c.mu.Lock()
						c.cancelRead = cancel
						c.mu.Unlock()
					}
					if isWriteError {
						c.setHook(func(_ context.Context, kind string) error {
							if kind == "write" {
								return status.PermissionDeniedError("injected storage write error")
							}
							return nil
						})
					}
					var err error
					if isUpload {
						deleteErr := c.Delete(ctx, rn)
						require.True(t, deleteErr == nil || status.IsNotFoundError(deleteErr), "%s", deleteErr)
						ul := cachetools.NewBatchCASUploader(callCtx, env, "", repb.DigestFunction_SHA256, nil)
						path := filepath.Join(root, "output")
						require.NoError(t, os.WriteFile(path, data, 0644))
						if strings.Contains(mode, "tree") {
							cmd := &repb.Command{OutputPaths: []string{"output"}}
							_, err = dirtools.UploadTree(callCtx, env, dirtools.NewDirHelper(root, cmd, 0755), "", repb.DigestFunction_SHA256, root, cmd, &repb.ActionResult{}, true, nil)
						} else {
							_, err = ul.UploadFile(path)
							require.NoError(t, err)
							err = ul.Wait()
						}
					} else {
						opts := &dirtools.DownloadTreeOpts{RootDir: root, ChunkedInputFiles: strings.Contains(mode, "chunked")}
						if mode == "filecache" || mode == "cancel_partial_filecache" {
							opts.RootDir = ""
						}
						_, err = dirtools.DownloadTree(callCtx, env, "", repb.DigestFunction_SHA256, tree, opts)
					}
					if mode == "missing" || mode == "disk_error" || isCancel || isWriteError {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
						for _, f := range tree.Root.Files {
							require.True(t, fc.ContainsFile(ctx, f), "completed file missing from filecache")
						}
					}
					// A cancelled singleflight caller can return before its worker's
					// asynchronous cleanup. Poll files only; count goroutines synchronously below.
					require.Eventually(t, func() bool { entries, err := os.ReadDir(fc.TempDir()); return err == nil && len(entries) == 0 }, testleak.DefaultSettle, 10*time.Millisecond, "stranded filecache temp files")
					temps, err := os.ReadDir(fc.TempDir())
					require.NoError(t, err)
					if i == 1 || i == 10 || i == 50 {
						fds, err := os.ReadDir("/proc/self/fd")
						require.NoError(t, err)
						t.Logf("sample task=%d goroutines=%d fds=%d filecache_temps=%d", i, runtime.NumGoroutine(), len(fds), len(temps))
					}
				}
				testleak.CheckGrowth(t, testleak.Growth{Iterations: n, Warmup: 3}, op)
			})
		}
	}
}

func TestCachedInputTreeLifecycle(t *testing.T) {
	for _, n := range []int{10, 50} {
		t.Run(fmt.Sprintf("N%d", n), func(t *testing.T) {
			testleak.Check(t)
			testleak.CheckFDs(t)
			flags.Set(t, "grpc_server_worker_multiplier", 0)
			flags.Set(t, "cache.request_cached_subtree_digests", true)
			env, ctx := testEnv(t)
			fc := env.GetFileCache()
			t.Cleanup(func() { require.NoError(t, fc.(io.Closer).Close()) })
			child := &repb.Directory{Files: []*repb.FileNode{{Name: "empty", Digest: &repb.Digest{Hash: digest.EmptySha256}}}}
			childDigest, err := cachetools.UploadProtoToCAS(ctx, env.GetCache(), "", repb.DigestFunction_SHA256, child)
			require.NoError(t, err)
			root := &repb.Directory{Directories: []*repb.DirectoryNode{{Name: "child", Digest: childDigest}}}
			rootDigest, err := cachetools.UploadProtoToCAS(ctx, env.GetCache(), "", repb.DigestFunction_SHA256, root)
			require.NoError(t, err)
			rn := digest.NewCASResourceName(rootDigest, "", repb.DigestFunction_SHA256)
			testleak.CheckGrowth(t, testleak.Growth{Iterations: n, Warmup: 3}, func(i int) {
				tree, err := cachetools.GetAndMaybeCacheTreeFromRootDirectoryDigest(ctx, env.GetContentAddressableStorageClient(), rn, fc, env.GetByteStreamClient())
				require.NoError(t, err)
				require.Len(t, tree.GetChildren(), 1)
				entries, err := os.ReadDir(fc.TempDir())
				require.NoError(t, err)
				require.Empty(t, entries)
				if i == 1 || i == 10 || i == 50 {
					fds, err := os.ReadDir("/proc/self/fd")
					require.NoError(t, err)
					t.Logf("sample task=%d goroutines=%d fds=%d filecache_temps=%d", i, runtime.NumGoroutine(), len(fds), len(entries))
				}
			})
		})
	}
}
