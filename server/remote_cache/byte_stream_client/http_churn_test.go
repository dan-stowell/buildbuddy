package byte_stream_client

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/buildbuddy_server"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/real_environment"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	_ "github.com/buildbuddy-io/buildbuddy/server/util/grpc_server"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
)

type httpChurnCache struct{ interfaces.Cache }
type httpChurnByteStream struct {
	bspb.UnimplementedByteStreamServer
	active atomic.Int64
	calls  atomic.Int64
}

func (s *httpChurnByteStream) Read(req *bspb.ReadRequest, stream bspb.ByteStream_ReadServer) error {
	s.active.Add(1)
	s.calls.Add(1)
	defer s.active.Add(-1)
	data := bytes.Repeat([]byte("x"), 65536)
	for range 32 {
		if err := stream.Send(&bspb.ReadResponse{Data: data}); err != nil {
			return err
		}
		if req.ResourceName == "blobs/error/2097152" {
			return status.UnavailableError("test mid-stream error")
		}
		if req.ResourceName == "blobs/disconnect/2097152" {
			<-stream.Context().Done()
			return stream.Context().Err()
		}
	}
	return nil
}

// TestHTTPDownloadClientChurn exercises the actual HTTP handler and pooled
// ByteStream client, including its retry paths. The cache marker selects the
// local gRPC endpoint; no cache implementation methods are used.
func TestHTTPDownloadClientChurn(t *testing.T) {
	for _, n := range []int{10, 50} {
		for _, mode := range []string{"complete", "error", "disconnect"} {
			t.Run(fmt.Sprintf("%s/N=%d", mode, n), func(t *testing.T) {
				testleak.Check(t)
				testleak.CheckFDs(t)
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				t.Cleanup(func() { listener.Close() })
				flags.Set(t, "grpc_server_worker_multiplier", 0)
				flags.Set(t, "grpc_port", listener.Addr().(*net.TCPAddr).Port)
				flags.Set(t, "grpc_client.enable_pool_cache", true)
				flags.Set(t, "app.restrict_bytestream_dialing", false)
				upstream := &httpChurnByteStream{}
				// No common-option worker pool: each blocked handler is visible.
				gs := grpc.NewServer()
				bspb.RegisterByteStreamServer(gs, upstream)
				go gs.Serve(listener)
				t.Cleanup(gs.Stop)
				env := real_environment.NewRealEnv(nil)
				env.SetCache(&httpChurnCache{})
				pooled := NewPooledByteStreamClient(env)
				env.SetPooledByteStreamClient(pooled)
				t.Cleanup(func() {
					for _, conn := range pooled.connPoolMap {
						require.NoError(t, conn.(io.Closer).Close())
					}
				})
				handler, err := buildbuddy_server.NewBuildBuddyServer(env, nil)
				require.NoError(t, err)
				mux := http.NewServeMux()
				mux.Handle("/file/download", handler)
				server := httptest.NewServer(mux)
				t.Cleanup(server.Close)
				transport := &http.Transport{DisableKeepAlives: true}
				t.Cleanup(transport.CloseIdleConnections)
				client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
				operation := func(i int) {
					before := upstream.calls.Load()
					target := "bytestream://" + listener.Addr().String() + "/blobs/" + mode + "/2097152"
					rsp, err := client.Get(server.URL + "/file/download?bytestream_url=" + url.QueryEscape(target))
					require.NoError(t, err)
					if mode == "disconnect" {
						_, err = io.ReadFull(rsp.Body, make([]byte, 128))
						require.NoError(t, err)
					} else {
						size, copyErr := io.Copy(io.Discard, rsp.Body)
						require.NoError(t, copyErr)
						if mode == "complete" {
							require.EqualValues(t, 2097152, size)
							require.Equal(t, http.StatusOK, rsp.StatusCode)
						}
					}
					require.NoError(t, rsp.Body.Close())
					require.Greater(t, upstream.calls.Load(), before, "upstream Read was not invoked")
				}
				// Prime both members of each lazy pool before taking a growth baseline.
				for i := range 4 {
					operation(i)
				}
				testleak.CheckGrowth(t, testleak.Growth{Iterations: n, Warmup: 4}, operation)
				require.Zero(t, upstream.active.Load())
				t.Logf("N=%d: settled goroutine and FD growth = 0; server and %d cached targets still alive", n, len(pooled.connPoolMap))
			})
		}
	}
}
