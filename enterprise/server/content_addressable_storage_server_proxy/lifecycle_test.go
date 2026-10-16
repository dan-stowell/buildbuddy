package content_addressable_storage_server_proxy

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

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/proto"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type lifecycleCASRemote struct {
	repb.UnimplementedContentAddressableStorageServer
	mode    string
	active  atomic.Int32
	started chan struct{}
}

func (s *lifecycleCASRemote) BatchReadBlobs(ctx context.Context, req *repb.BatchReadBlobsRequest) (*repb.BatchReadBlobsResponse, error) {
	s.active.Add(1)
	defer s.active.Add(-1)
	s.started <- struct{}{}
	if s.mode == "remote_error" {
		return nil, status.Error(codes.Unavailable, "injected remote failure")
	}
	if s.mode != "success" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	data, err := proto.Marshal(&repb.Directory{Files: []*repb.FileNode{{Name: req.InstanceName}}})
	if err != nil {
		return nil, err
	}
	return &repb.BatchReadBlobsResponse{Responses: []*repb.BatchReadBlobsResponse_Response{{Digest: req.Digests[0], Data: data, Status: status.New(codes.OK, "").Proto()}}}, nil
}
func (s *lifecycleCASRemote) GetTree(req *repb.GetTreeRequest, stream repb.ContentAddressableStorage_GetTreeServer) error {
	s.active.Add(1)
	defer s.active.Add(-1)
	if err := stream.Send(&repb.GetTreeResponse{Directories: []*repb.Directory{{}}}); err != nil {
		return err
	}
	if s.mode == "success" {
		return nil
	}
	if s.mode == "remote_error" {
		return status.Error(codes.Unavailable, "injected mid-stream failure")
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}
func casLifecycleFDs(t *testing.T) []string {
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
func TestCASRequestLifecycle(t *testing.T) {
	for _, op := range []string{"batch_read", "get_tree", "get_tree_cached"} {
		for _, mode := range []string{"success", "cancel", "remote_error", "deadline"} {
			t.Run(op+"/"+mode, func(t *testing.T) {
				testleak.Check(t)
				fdBefore := casLifecycleFDs(t)
				t.Cleanup(func() {
					require.Eventually(t, func() bool { return slices.Equal(casLifecycleFDs(t), fdBefore) }, 5*time.Second, 10*time.Millisecond, "FDs before=%v after=%v", fdBefore, casLifecycleFDs(t))
					t.Logf("after shutdown: goroutines=%d FDs=%d", runtime.NumGoroutine(), len(casLifecycleFDs(t)))
				})
				remoteEnv, proxyEnv := testenv.GetTestEnv(t), testenv.GetTestEnv(t)
				flags.Set(t, "cache_proxy.enable_get_tree_caching", op == "get_tree_cached")
				remote := &lifecycleCASRemote{mode: mode, started: make(chan struct{}, 100)}
				server, run, lis := testenv.RegisterLocalGRPCServer(t, remoteEnv)
				repb.RegisterContentAddressableStorageServer(server, remote)
				go run()
				ctx := context.Background()
				conn, err := testenv.LocalGRPCConn(t, ctx, lis)
				require.NoError(t, err)
				t.Cleanup(func() { conn.Close() })
				proxy := repb.NewContentAddressableStorageClient(runCASProxy(ctx, conn, proxyEnv, t))
				runRequest := func(i int) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					if mode == "deadline" {
						cancel()
						ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
					}
					defer cancel()
					// Use a distinct blob each time: CAS caches may share content
					// across instance names, so a fresh instance alone is insufficient.
					instance := fmt.Sprintf("request-%d", i)
					data, marshalErr := proto.Marshal(&repb.Directory{Files: []*repb.FileNode{{Name: instance}}})
					require.NoError(t, marshalErr)
					d, computeErr := digest.Compute(bytes.NewReader(data), repb.DigestFunction_SHA256)
					require.NoError(t, computeErr)
					var err error
					if op == "batch_read" {
						done := make(chan error, 1)
						go func() {
							_, e := proxy.BatchReadBlobs(ctx, &repb.BatchReadBlobsRequest{InstanceName: instance, Digests: []*repb.Digest{d}, DigestFunction: repb.DigestFunction_SHA256})
							done <- e
						}()
						select {
						case <-remote.started:
						case <-time.After(5 * time.Second):
							t.Fatal("remote request never started")
						}
						if mode == "cancel" {
							cancel()
						}
						err = <-done
					} else {
						stream, e := proxy.GetTree(ctx, &repb.GetTreeRequest{InstanceName: instance, RootDigest: d, DigestFunction: repb.DigestFunction_SHA256})
						require.NoError(t, e)
						if op == "get_tree_cached" && mode != "success" {
							select {
							case <-remote.started:
							case <-time.After(5 * time.Second):
								t.Fatal("remote request never started")
							}
							if mode == "cancel" {
								cancel()
							}
							_, err = stream.Recv()
						} else {
							_, err = stream.Recv()
							require.NoError(t, err)
							if mode == "cancel" {
								cancel()
							}
							_, err = stream.Recv()
						}
						if mode == "success" {
							require.Equal(t, io.EOF, err)
							err = nil
						}
					}
					if mode == "success" {
						require.NoError(t, err)
					} else {
						require.Error(t, err)
					}
					require.Eventually(t, func() bool { return remote.active.Load() == 0 }, 5*time.Second, 10*time.Millisecond, "remote RPC did not end")
				}
				runRequest(-1)
				baseG, baseFD := runtime.NumGoroutine(), len(casLifecycleFDs(t))
				t.Logf("N=0 goroutines=%d FDs=%d targets=%v", baseG, baseFD, casLifecycleFDs(t))
				done := 0
				for _, n := range []int{1, 10, 50} {
					for done < n {
						runRequest(done)
						done++
					}
					// Poll synchronously: Eventually adds a goroutine to the predicate.
					deadline := time.Now().Add(5 * time.Second)
					ticker := time.NewTicker(10 * time.Millisecond)
					for runtime.NumGoroutine() > baseG || len(casLifecycleFDs(t)) > baseFD {
						if time.Now().After(deadline) {
							buf := make([]byte, 1<<20)
							count := runtime.Stack(buf, true)
							t.Fatalf("growth after N=%d: baseline G=%d FDs=%d; current G=%d targets=%v\n%s", n, baseG, baseFD, runtime.NumGoroutine(), casLifecycleFDs(t), buf[:count])
						}
						<-ticker.C
					}
					ticker.Stop()
					t.Logf("N=%d goroutines=%d FDs=%d remote_active=%d", n, runtime.NumGoroutine(), len(casLifecycleFDs(t)), remote.active.Load())
				}
			})
		}
	}
}
