//go:build linux && !android

package vfs_server_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/container"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/filecache"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/vfs_server"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/vfscommon"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	vfspb "github.com/buildbuddy-io/buildbuddy/proto/vfs"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/byte_stream_server"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// blockedReadServer models a CAS stream that is waiting for data when the task ends.
type blockedReadServer struct {
	bspb.ByteStreamServer
	started chan struct{}
}

func (s *blockedReadServer) Read(req *bspb.ReadRequest, stream bspb.ByteStream_ReadServer) error {
	s.started <- struct{}{}
	<-stream.Context().Done()
	return stream.Context().Err()
}

// Use a separate target: the ordinary VFS tests have unrelated service owners.
func TestTaskHandleLifecycle(t *testing.T) {
	for _, mode := range []string{"release", "guest_disappears", "task_cancelled", "cas_read", "cas_missing", "cas_cancelled"} {
		for _, n := range []int{10, 50} {
			t.Run(fmt.Sprintf("%s/N%d", mode, n), func(t *testing.T) {
				testleak.Check(t)
				testleak.CheckFDs(t)
				flags.Set(t, "grpc_server_worker_multiplier", 0)
				env := testenv.GetTestEnv(t)
				workspace := t.TempDir()
				fc, err := filecache.NewFileCache(t.TempDir(), 1<<20, false)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, fc.Close()) })
				fc.WaitForDirectoryScanToComplete()
				env.SetFileCache(fc)
				bs, err := byte_stream_server.NewByteStreamServer(env)
				require.NoError(t, err)
				gs, serve, blis := testenv.RegisterLocalGRPCServer(t, env)
				started := make(chan struct{}, 1)
				if mode == "cas_cancelled" {
					bspb.RegisterByteStreamServer(gs, &blockedReadServer{ByteStreamServer: bs, started: started})
				} else {
					bspb.RegisterByteStreamServer(gs, bs)
				}
				go serve()
				baseCtx, err := prefix.AttachUserPrefixToContext(context.Background(), env.GetAuthenticator())
				require.NoError(t, err)
				bc, err := testenv.LocalGRPCConn(t, baseCtx, blis)
				require.NoError(t, err)
				env.SetByteStreamClient(bspb.NewByteStreamClient(bc))
				blob := []byte("lazy input data")
				d, err := digest.Compute(strings.NewReader(string(blob)), repb.DigestFunction_SHA256)
				require.NoError(t, err)
				rn := digest.NewCASResourceName(d, "", repb.DigestFunction_SHA256)
				require.NoError(t, env.GetCache().Set(baseCtx, rn.ToProto(), blob))
				handles := func() int {
					entries, err := os.ReadDir("/proc/self/fd")
					require.NoError(t, err)
					count := 0
					for _, e := range entries {
						target, err := os.Readlink("/proc/self/fd/" + e.Name())
						if err == nil && strings.HasPrefix(target, workspace+"/") {
							count++
						}
					}
					return count
				}
				testleak.CheckGrowth(t, testleak.Growth{Iterations: n}, func(i int) {
					if mode == "cas_read" {
						env.GetFileCache().DeleteFile(baseCtx, &repb.FileNode{Digest: d})
					}
					ctx, cancel := context.WithTimeout(baseCtx, 15*time.Second)
					defer cancel()
					s, err := vfs_server.New(env, workspace)
					require.NoError(t, err)
					root := &repb.Directory{}
					if strings.HasPrefix(mode, "cas_") {
						nodeDigest := d
						if mode == "cas_missing" {
							nodeDigest = &repb.Digest{Hash: strings.Repeat("a", 64), SizeBytes: 123}
						}
						root.Files = []*repb.FileNode{{Name: "input", Digest: nodeDigest}}
					}
					_, err = s.Prepare(ctx, &container.FileSystemLayout{Inputs: &repb.Tree{Root: root}, DigestFunction: repb.DigestFunction_SHA256}, nil)
					require.NoError(t, err)
					lis, err := net.Listen("tcp", "127.0.0.1:0")
					require.NoError(t, err)
					require.NoError(t, s.Start(lis))
					conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
					require.NoError(t, err)
					// Defers cover assertion failures; the explicit shutdown below defines the measurement boundary.
					defer conn.Close()
					stopped := false
					defer func() {
						if !stopped {
							s.Stop()
						}
					}()
					client := vfspb.NewFileSystemClient(conn)
					if strings.HasPrefix(mode, "cas_") {
						lookup, err := client.Lookup(ctx, &vfspb.LookupRequest{ParentId: vfscommon.RootInodeId, Name: "input"})
						require.NoError(t, err)
						var opened *vfspb.OpenResponse
						if mode == "cas_cancelled" {
							done := make(chan error, 1)
							go func() {
								_, err := client.Open(baseCtx, &vfspb.OpenRequest{Id: lookup.Id})
								done <- err
							}()
							select {
							case <-started:
							case <-time.After(10 * time.Second):
								t.Fatal("CAS Read did not start")
							}
							cancel()
							select {
							case err = <-done:
							case <-time.After(10 * time.Second):
								t.Fatal("Open did not finish after task cancellation")
							}
						} else {
							opened, err = client.Open(ctx, &vfspb.OpenRequest{Id: lookup.Id})
						}
						if mode == "cas_read" {
							require.NoError(t, err)
							read, err := client.Read(ctx, &vfspb.ReadRequest{HandleId: opened.HandleId, NumBytes: 100})
							require.NoError(t, err)
							require.Equal(t, blob, read.Data)
						} else {
							require.Error(t, err)
						}
					} else {
						for k := range 4 {
							r, err := client.Create(ctx, &vfspb.CreateRequest{ParentId: vfscommon.RootInodeId, Name: fmt.Sprintf("out%d", k), Flags: syscall.O_RDWR | syscall.O_CREAT, Mode: 0600})
							require.NoError(t, err)
							_, err = client.Write(ctx, &vfspb.WriteRequest{HandleId: r.HandleId, Data: []byte("hello")})
							require.NoError(t, err)
							_, err = client.Read(ctx, &vfspb.ReadRequest{HandleId: r.HandleId, NumBytes: 5})
							require.NoError(t, err)
							_, err = client.SetLk(ctx, &vfspb.SetLkRequest{HandleId: r.HandleId, FileLock: &vfspb.FileLock{Typ: syscall.F_WRLCK, End: 0x7fffffffffffffff}})
							require.NoError(t, err)
							if mode == "release" {
								_, err = client.Release(ctx, &vfspb.ReleaseRequest{HandleId: r.HandleId})
								require.NoError(t, err)
							}
						}
					}
					if !strings.HasPrefix(mode, "cas_") {
						want := 8 // Four file handles, each with a separate lock FD.
						if mode == "release" {
							want = 0
						}
						require.Equal(t, want, handles(), "prove handles exist before testing task teardown")
					}
					if mode == "task_cancelled" {
						cancel()
					}
					if mode == "guest_disappears" {
						require.NoError(t, conn.Close())
					}
					s.Stop()
					stopped = true
					_ = conn.Close()
					require.Zero(t, handles(), "task-owned workspace and lock FDs after Stop")
					require.NoError(t, os.RemoveAll(workspace))
					require.NoError(t, os.MkdirAll(workspace, 0700))
					if i == 0 || i == 10 || i == 50 {
						t.Logf("completed=%d workspace_fds=%d goroutines=%d", i, handles(), runtime.NumGoroutine())
					}
				})
			})
		}
	}
}
