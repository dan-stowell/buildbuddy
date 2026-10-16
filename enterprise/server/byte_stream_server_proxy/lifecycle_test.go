package byte_stream_server_proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/backends/pebble_cache"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	rspb "github.com/buildbuddy-io/buildbuddy/proto/resource"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/require"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
)

// lifecycleRemote deliberately keeps a partially consumed stream open until its
// RPC context ends. This makes remote-handler completion an ownership check,
// independently of process-wide resource counts.
type lifecycleRemote struct {
	bspb.UnimplementedByteStreamServer
	mode       string
	data       []byte
	active     atomic.Int32
	started    chan struct{}
	chunks     map[string][]byte
	firstChunk string
}

func (s *lifecycleRemote) Read(req *bspb.ReadRequest, stream bspb.ByteStream_ReadServer) error {
	s.active.Add(1)
	defer s.active.Add(-1)
	data := s.data
	mode := s.mode
	if s.chunks != nil {
		rn, err := digest.ParseDownloadResourceName(req.ResourceName)
		if err != nil {
			return err
		}
		data = s.chunks[rn.GetDigest().GetHash()]
		if rn.GetDigest().GetHash() == s.firstChunk {
			mode = "success"
		}
	}
	if err := stream.Send(&bspb.ReadResponse{Data: data}); err != nil {
		return err
	}
	if mode == "success" {
		return nil
	}
	if mode == "remote_error" {
		return status.UnavailableError("injected mid-read failure")
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}
func (s *lifecycleRemote) Write(stream bspb.ByteStream_WriteServer) error {
	s.active.Add(1)
	defer s.active.Add(-1)
	var size int64
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}
		size += int64(len(req.Data))
		s.started <- struct{}{}
		if s.mode == "remote_error" {
			return status.UnavailableError("injected mid-write failure")
		}
		if req.FinishWrite {
			return stream.SendAndClose(&bspb.WriteResponse{CommittedSize: size})
		}
	}
}

type lifecycleSplitClient struct {
	repb.ContentAddressableStorageClient
	chunks []*repb.Digest
}

func (c *lifecycleSplitClient) SplitBlob(ctx context.Context, req *repb.SplitBlobRequest, opts ...grpc.CallOption) (*repb.SplitBlobResponse, error) {
	return &repb.SplitBlobResponse{ChunkDigests: c.chunks}, nil
}

// Fail after opening and partially writing the real cache writer, so its
// underlying file/compressor must still be closed by the local server.
type lifecycleFailingCache struct{ interfaces.Cache }

func (c *lifecycleFailingCache) Writer(ctx context.Context, r *rspb.ResourceName) (interfaces.CommittedWriteCloser, error) {
	w, err := c.Cache.Writer(ctx, r)
	if err != nil {
		return nil, err
	}
	return &lifecycleFailingWriter{CommittedWriteCloser: w}, nil
}

type lifecycleFailingWriter struct {
	interfaces.CommittedWriteCloser
}

func (w *lifecycleFailingWriter) Write(p []byte) (int, error) {
	n, err := w.CommittedWriteCloser.Write(p[:len(p)/2])
	if err != nil {
		return n, err
	}
	return n, status.UnavailableError("injected local cache write failure")
}

// Keep the FD probe local until testleak gains FD support. Readlink omits the
// transient directory FD opened by ReadDir. Preserve multiplicity of targets.
func lifecycleFDs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	var targets []string
	for _, e := range entries {
		target, err := os.Readlink("/proc/self/fd/" + e.Name())
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets
}

func TestProxyStreamLifecycle(t *testing.T) {
	for _, op := range []string{"read", "write", "read_chunked"} {
		for _, mode := range []string{"success", "cancel", "remote_error", "deadline", "local_error"} {
			t.Run(op+"/"+mode, func(t *testing.T) {
				testleak.Check(t)
				fdBefore := lifecycleFDs(t)
				// Registered before the environments so this checks after their shutdown.
				t.Cleanup(func() {
					require.Eventually(t, func() bool { return slices.Equal(lifecycleFDs(t), fdBefore) }, 5*time.Second, 10*time.Millisecond, "FDs before=%v after=%v", fdBefore, lifecycleFDs(t))
					t.Logf("after shutdown: goroutines=%d FDs=%d", runtime.NumGoroutine(), len(lifecycleFDs(t)))
				})
				remoteEnv := testenv.GetTestEnv(t)
				proxyEnv := testenv.GetTestEnv(t)
				pc, err := pebble_cache.NewPebbleCache(proxyEnv, &pebble_cache.Options{RootDirectory: t.TempDir(), MaxSizeBytes: 1_000_000_000})
				require.NoError(t, err)
				require.NoError(t, pc.Start())
				t.Cleanup(func() { require.NoError(t, pc.Stop()) })
				proxyEnv.SetCache(pc)
				if mode == "local_error" {
					proxyEnv.SetCache(&lifecycleFailingCache{Cache: pc})
				}
				remote := &lifecycleRemote{mode: mode, data: bytes.Repeat([]byte("x"), 128*1024), started: make(chan struct{}, 2)}
				if mode == "local_error" {
					remote.mode = "success"
				}
				server, run, lis := testenv.RegisterLocalGRPCServer(t, remoteEnv)
				bspb.RegisterByteStreamServer(server, remote)
				go run()
				conn, err := testenv.LocalGRPCConn(t, testContext(), lis)
				require.NoError(t, err)
				t.Cleanup(func() { conn.Close() })
				blobData := remote.data
				if op == "read_chunked" {
					split := &lifecycleSplitClient{}
					remote.chunks = make(map[string][]byte)
					blobData = nil
					for j := range 5 {
						data := bytes.Repeat([]byte{byte('a' + j)}, 1024*1024)
						cd, err := digest.Compute(bytes.NewReader(data), repb.DigestFunction_SHA256)
						require.NoError(t, err)
						split.chunks = append(split.chunks, cd)
						remote.chunks[cd.Hash] = data
						if j == 0 {
							remote.firstChunk = cd.Hash
						}
						blobData = append(blobData, data...)
					}
					proxyEnv.SetContentAddressableStorageClient(split)
				}
				proxy := runBSProxy(testContext(), bspb.NewByteStreamClient(conn), proxyEnv, t)
				d, err := digest.Compute(bytes.NewReader(blobData), repb.DigestFunction_SHA256)
				require.NoError(t, err)
				runRequest := func(i int) {
					ctx, cancel := context.WithTimeout(testContext(), 5*time.Second)
					if mode == "deadline" {
						cancel()
						ctx, cancel = context.WithTimeout(testContext(), 100*time.Millisecond)
					}
					defer cancel()
					rn := digest.NewCASResourceName(d, fmt.Sprintf("request-%d", i), repb.DigestFunction_SHA256)
					if op == "read" || op == "read_chunked" {
						stream, err := proxy.Read(ctx, &bspb.ReadRequest{ResourceName: rn.DownloadString()})
						require.NoError(t, err)
						_, err = stream.Recv()
						require.NoError(t, err)
						if mode == "cancel" {
							cancel()
						}
						for {
							_, err = stream.Recv()
							if err != nil {
								break
							}
						}
						if mode == "success" || mode == "local_error" {
							require.Equal(t, io.EOF, err)
						} else {
							require.Error(t, err)
						}
					} else {
						stream, err := proxy.Write(ctx)
						require.NoError(t, err)
						require.NoError(t, stream.Send(&bspb.WriteRequest{ResourceName: rn.NewUploadString(), Data: remote.data, FinishWrite: (mode == "success" || mode == "local_error")}))
						select {
						case <-remote.started:
						case <-time.After(5 * time.Second):
							t.Fatal("remote never received first write frame")
						}
						if mode == "cancel" {
							cancel()
						}
						if mode == "deadline" {
							<-ctx.Done()
						}
						_, err = stream.CloseAndRecv()
						if mode == "success" || mode == "local_error" {
							require.NoError(t, err)
						} else {
							require.Error(t, err)
						}
					}
					require.Eventually(t, func() bool { return remote.active.Load() == 0 }, 5*time.Second, 10*time.Millisecond, "remote RPC still owned after client finished")
				}
				runRequest(-1) // Warm transport connections and lazy cache initialization.
				baseG, baseFD := runtime.NumGoroutine(), len(lifecycleFDs(t))
				t.Logf("N=0 goroutines=%d FDs=%d targets=%v", baseG, baseFD, lifecycleFDs(t))
				done := 0
				for _, n := range []int{1, 10, 50} {
					for done < n {
						runRequest(done)
						done++
					}
					// Eventually runs its predicate in a new goroutine, perturbing the
					// count being measured. Poll synchronously instead.
					deadline := time.Now().Add(5 * time.Second)
					ticker := time.NewTicker(10 * time.Millisecond)
					for runtime.NumGoroutine() > baseG || len(lifecycleFDs(t)) > baseFD {
						if time.Now().After(deadline) {
							buf := make([]byte, 1<<20)
							count := runtime.Stack(buf, true)
							t.Fatalf("growth after N=%d: baseline G=%d FDs=%d; current G=%d targets=%v\n%s", n, baseG, baseFD, runtime.NumGoroutine(), lifecycleFDs(t), buf[:count])
						}
						<-ticker.C
					}
					ticker.Stop()
					t.Logf("N=%d goroutines=%d FDs=%d remote_active=%d", n, runtime.NumGoroutine(), len(lifecycleFDs(t)), remote.active.Load())
				}
			})
		}
	}
}
