package protolet

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/stretchr/testify/require"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
)

type churnByteStream struct {
	bspb.UnimplementedByteStreamServer
	mode     string
	finished chan struct{}
	teardown chan struct{}
}

func (s *churnByteStream) Read(_ *bspb.ReadRequest, stream bspb.ByteStream_ReadServer) error {
	defer func() { s.finished <- struct{}{} }()
	if err := stream.Send(&bspb.ReadResponse{Data: bytes.Repeat([]byte("x"), 32768)}); err != nil {
		return err
	}
	if s.mode == "disconnect" {
		select {
		case <-stream.Context().Done():
		case <-s.teardown:
		}
		return stream.Context().Err()
	}
	return nil
}

// Exercise the same prefixed-proto HTTP bridge used by browser streaming
// RPCs, with a real registered server-streaming RPC.
func TestStreamingHTTPClientChurn(t *testing.T) {
	testleak.Check(t)
	testleak.CheckFDs(t)
	service := &churnByteStream{finished: make(chan struct{}, 1), teardown: make(chan struct{})}
	// Plain grpc.NewServer has no worker pool hiding blocked handlers.
	gs := grpc.NewServer()
	defer gs.Stop()
	bspb.RegisterByteStreamServer(gs, service)
	handlers, err := GenerateHTTPHandlers("/rpc/", "google.bytestream.ByteStream", service, gs)
	require.NoError(t, err)
	server := httptest.NewServer(handlers.BodyParserMiddleware(handlers.RequestHandler))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(service.teardown) })
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	operation := func(int) {
		req, err := http.NewRequest("POST", server.URL+"/rpc/Read", bytes.NewReader(make([]byte, 5)))
		require.NoError(t, err)
		req.Header.Set("Content-Type", prefixedProtoContentType)
		rsp, err := client.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rsp.StatusCode)
		if service.mode == "disconnect" {
			_, err = io.ReadFull(rsp.Body, make([]byte, 128))
		} else {
			_, err = io.Copy(io.Discard, rsp.Body)
		}
		require.NoError(t, err)
		require.NoError(t, rsp.Body.Close())
		select {
		case <-service.finished:
		case <-time.After(testleak.DefaultSettle):
			t.Fatal("RPC did not finish after HTTP disconnect")
		}
	}
	operation(0)
	service.mode = "disconnect"
	testleak.CheckGrowth(t, testleak.Growth{Iterations: 10}, operation)
}
