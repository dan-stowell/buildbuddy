package dirtools_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/filecache"
	"github.com/buildbuddy-io/buildbuddy/server/cache/dirtools"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/byte_stream_server"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/cachetools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/content_addressable_storage_server"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testdigest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	rspb "github.com/buildbuddy-io/buildbuddy/proto/resource"
	bspb "google.golang.org/genproto/googleapis/bytestream"
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
	for _, mode := range []string{"cancel_partial_stream", "cancel_upload", "cancel_chunked"} {
		t.Run(mode, func(t *testing.T) {
			testleak.Check(t)
			testleak.CheckFDs(t)
			flags.Set(t, "grpc_server_worker_multiplier", 0)
			var c *lifecycleCache
			env, ctx := transferEnv(t, func(base interfaces.Cache) interfaces.Cache {
				c = &lifecycleCache{Cache: base}
				return c
			})
			fc := env.GetFileCache()
			t.Cleanup(func() { require.NoError(t, fc.(io.Closer).Close()) })
			chunked := mode == "cancel_chunked"
			size := int(dirtools.BatchReadLimitBytes) + 1
			if chunked {
				size = 6 * 1024 * 1024
			}
			rn, data := testdigest.RandomCASResourceBuf(t, int64(size))
			require.NoError(t, c.Set(ctx, rn, data))
			node := &repb.FileNode{Name: "input", Digest: rn.GetDigest()}
			if chunked {
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
			root := testfs.MakeTempDir(t)
			// Finish real RPCs before measuring, including server-side transport setup.
			for range 3 {
				require.NoError(t, cachetools.GetBlob(ctx, env.GetByteStreamClient(), digest.NewCASResourceName(node.Digest, "", repb.DigestFunction_SHA256), io.Discard))
			}
			testleak.CheckGrowth(t, testleak.Growth{Iterations: 10, Warmup: 3}, func(int) {
				c.setHook(nil)
				callCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				fc.DeleteFile(ctx, node)
				require.NoError(t, os.RemoveAll(root))
				require.NoError(t, os.MkdirAll(root, 0755))
				var err error
				if mode == "cancel_upload" {
					c.setHook(func(serverCtx context.Context, kind string) error {
						if kind != "write" {
							return nil
						}
						cancel()
						<-serverCtx.Done()
						return status.FromContextError(serverCtx)
					})
					deleteErr := c.Delete(ctx, rn)
					require.True(t, deleteErr == nil || status.IsNotFoundError(deleteErr), "%s", deleteErr)
					path := filepath.Join(root, "output")
					require.NoError(t, os.WriteFile(path, data, 0644))
					ul := cachetools.NewBatchCASUploader(callCtx, env, "", repb.DigestFunction_SHA256, nil)
					_, err = ul.UploadFile(path)
					require.NoError(t, err)
					err = ul.Wait()
				} else {
					c.mu.Lock()
					c.cancelRead = cancel
					c.mu.Unlock()
					_, err = dirtools.DownloadTree(callCtx, env, "", repb.DigestFunction_SHA256, tree, &dirtools.DownloadTreeOpts{RootDir: root, ChunkedInputFiles: chunked})
				}
				require.Error(t, err)
				if mode == "cancel_upload" {
					// Wait has returned, so the source must be closed already. Check
					// before settling: a finalizer could close a leaked *os.File later.
					require.Empty(t, openFilesUnder(t, root), "upload left its source open")
				}
				// A cancelled singleflight caller returns before its worker finishes
				// writing to and closing the destination.
				require.Eventually(t, func() bool {
					entries, err := os.ReadDir(fc.TempDir())
					return err == nil && len(entries) == 0 && len(openFilesUnder(t, root, fc.TempDir())) == 0
				}, testleak.DefaultSettle, 10*time.Millisecond, "stranded filecache temp files or open transfer files")
			})
		})
	}
}

// openFilesUnder returns the targets of this process's FDs under any of dirs.
func openFilesUnder(t *testing.T, dirs ...string) []string {
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	var open []string
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil {
			continue // Closed since ReadDir, e.g. the ReadDir FD itself.
		}
		for _, dir := range dirs {
			if strings.HasPrefix(target, dir+"/") {
				open = append(open, target)
			}
		}
	}
	return open
}

func transferEnv(t *testing.T, wrap func(interfaces.Cache) interfaces.Cache) (*testenv.TestEnv, context.Context) {
	env := testenv.GetTestEnv(t)
	env.SetCache(wrap(env.GetCache()))
	ctx, err := prefix.AttachUserPrefixToContext(context.Background(), env.GetAuthenticator())
	require.NoError(t, err)
	casServer, err := content_addressable_storage_server.NewContentAddressableStorageServer(env)
	require.NoError(t, err)
	byteStreamServer, err := byte_stream_server.NewByteStreamServer(env)
	require.NoError(t, err)
	grpcServer, runFunc, lis := testenv.RegisterLocalGRPCServer(t, env)
	repb.RegisterContentAddressableStorageServer(grpcServer, casServer)
	bspb.RegisterByteStreamServer(grpcServer, byteStreamServer)
	go runFunc()
	conn, err := testenv.LocalGRPCConn(t, ctx, lis)
	require.NoError(t, err)
	env.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))
	env.SetByteStreamClient(bspb.NewByteStreamClient(conn))
	fc, err := filecache.NewFileCache(testfs.MakeTempDir(t), 10e9, false)
	require.NoError(t, err)
	fc.WaitForDirectoryScanToComplete()
	env.SetFileCache(fc)
	return env, ctx
}
