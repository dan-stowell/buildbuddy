package protolet

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/require"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
)

type churnByteStream struct {
	bspb.UnimplementedByteStreamServer
	mode     string
	finished chan struct{}
}

func (s *churnByteStream) Read(_ *bspb.ReadRequest, stream bspb.ByteStream_ReadServer) error {
	defer func() { s.finished <- struct{}{} }()
	if err := stream.Send(&bspb.ReadResponse{Data: bytes.Repeat([]byte("x"), 32768)}); err != nil {
		return err
	}
	if s.mode == "disconnect" {
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	if s.mode == "error" {
		return status.UnavailableError("test upstream error")
	}
	return nil
}

// Exercise the same prefixed-proto HTTP bridge used by browser streaming
// RPCs, with a real registered server-streaming RPC.
func TestStreamingHTTPClientChurn(t *testing.T) {
	for _, n := range []int{10, 50} {
		for _, mode := range []string{"complete", "error", "disconnect"} {
			t.Run(fmt.Sprintf("%s/N=%d", mode, n), func(t *testing.T) {
				testleak.Check(t)
				testleak.CheckFDs(t)
				service := &churnByteStream{mode: mode, finished: make(chan struct{}, 1)}
				// Plain grpc.NewServer has no BuildBuddy worker pool hiding handlers.
				gs := grpc.NewServer()
				defer gs.Stop()
				bspb.RegisterByteStreamServer(gs, service)
				handlers, err := GenerateHTTPHandlers("/rpc/", "google.bytestream.ByteStream", service, gs)
				require.NoError(t, err)
				server := httptest.NewServer(handlers.BodyParserMiddleware(handlers.RequestHandler))
				defer server.Close()
				transport := &http.Transport{DisableKeepAlives: true}
				defer transport.CloseIdleConnections()
				client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
				testleak.CheckGrowth(t, testleak.Growth{Iterations: n}, func(i int) {
					// Empty ReadRequest encoded as a gRPC message.
					req, err := http.NewRequest("POST", server.URL+"/rpc/Read", bytes.NewReader(make([]byte, 5)))
					require.NoError(t, err)
					req.Header.Set("Content-Type", prefixedProtoContentType)
					rsp, err := client.Do(req)
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, rsp.StatusCode)
					if mode == "disconnect" {
						_, err = io.ReadFull(rsp.Body, make([]byte, 128))
						require.NoError(t, err)
					} else {
						_, err = io.Copy(io.Discard, rsp.Body)
						require.NoError(t, err)
					}
					require.NoError(t, rsp.Body.Close())
					select {
					case <-service.finished:
					case <-time.After(10 * time.Second):
						t.Fatal("RPC did not finish after HTTP disconnect")
					}
				})
				t.Logf("N=%d: settled goroutine and FD growth = 0; server still alive", n)
			})
		}
	}
}
