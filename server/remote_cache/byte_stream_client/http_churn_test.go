package byte_stream_client

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/buildbuddy_server"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/real_environment"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	_ "github.com/buildbuddy-io/buildbuddy/server/util/grpc_server"
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
	mode := "complete"
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
	// Stop upstream handlers before waiting for the HTTP server to close.
	t.Cleanup(gs.Stop)
	transport := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	operation := func(int) {
		before := upstream.calls.Load()
		target := "bytestream://" + listener.Addr().String() + "/blobs/" + mode + "/2097152"
		rsp, err := client.Get(server.URL + "/file/download?bytestream_url=" + url.QueryEscape(target))
		require.NoError(t, err)
		if mode == "disconnect" {
			_, err = io.ReadFull(rsp.Body, make([]byte, 128))
			require.NoError(t, err)
		} else {
			size, err := io.Copy(io.Discard, rsp.Body)
			require.NoError(t, err)
			require.EqualValues(t, 2097152, size)
			require.Equal(t, http.StatusOK, rsp.StatusCode)
		}
		require.NoError(t, rsp.Body.Close())
		require.Greater(t, upstream.calls.Load(), before, "upstream Read was not invoked")
		// Each upstream Read must end before the next operation, so that no
		// exiting handler from one iteration can offset another's growth.
		require.Eventually(t, func() bool { return upstream.active.Load() == 0 }, testleak.DefaultSettle, 10*time.Millisecond, "upstream Read still active after the HTTP request ended")
	}
	// Cancellation triggers fallback dialing too. Prime both plaintext aliases
	// with completed RPCs, and finish failed TLS handshakes before measuring.
	// Otherwise an asynchronously initialized fallback transport can look like growth.
	for _, host := range []string{net.JoinHostPort("localhost", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)), listener.Addr().String()} {
		u, err := url.Parse("bytestream://" + host + "/blobs/complete/2097152")
		require.NoError(t, err)
		for range 4 {
			ctx, cancel := context.WithTimeout(t.Context(), testleak.DefaultSettle)
			require.NoError(t, pooled.streamFromUrl(ctx, u, false, 0, 0, io.Discard))
			require.Error(t, pooled.streamFromUrl(ctx, u, true, 0, 0, io.Discard))
			cancel()
		}
	}
	// Complete RPCs on the lazy pool before measuring cancelled downloads.
	for range 4 {
		operation(0)
	}
	mode = "disconnect"
	testleak.CheckGrowth(t, testleak.Growth{Iterations: 10, Warmup: 4}, operation)
}
