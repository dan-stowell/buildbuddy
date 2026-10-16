package operation

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// resourceExecutionServer accepts PublishOperation streams. With retry set,
// every other stream fails after the first message, like an app disconnect,
// so the publisher has to retry on a new stream.
type resourceExecutionServer struct {
	repb.UnimplementedExecutionServer
	opened atomic.Int64
	retry  bool
}

func (s *resourceExecutionServer) PublishOperation(stream repb.Execution_PublishOperationServer) error {
	fail := s.retry && s.opened.Add(1)%2 == 1
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&repb.PublishOperationResponse{})
		}
		if err != nil {
			return err
		}
		if fail {
			return status.Error(codes.Unavailable, "injected app disconnect")
		}
	}
}

// Publishing must not accumulate goroutines or FDs while the client
// connection and server stay up, whether the publish completes, is cancelled,
// or retries, and nothing may remain once they shut down.
func TestPublisherLifecycleResources(t *testing.T) {
	for _, mode := range []string{"success", "cancel", "retry"} {
		t.Run(mode, func(t *testing.T) {
			testleak.Check(t)
			testleak.CheckFDs(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			srv := grpc.NewServer()
			repb.RegisterExecutionServer(srv, &resourceExecutionServer{retry: mode == "retry"})
			go srv.Serve(listener)
			t.Cleanup(srv.Stop)
			conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.Close() })
			client := repb.NewExecutionClient(conn)
			testleak.CheckGrowth(t, testleak.Growth{Iterations: 10}, func(int) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				pub, err := Publish(ctx, client, testTaskID)
				if err != nil {
					t.Fatal(err)
				}
				op, err := Assemble(testTaskID, Metadata(repb.ExecutionStage_COMPLETED, testDigest), &repb.ExecuteResponse{})
				if err != nil {
					t.Fatal(err)
				}
				if err := pub.Send(op); err != nil {
					t.Fatal(err)
				}
				if mode == "cancel" {
					cancel()
				}
				if _, err := pub.CloseAndRecv(); mode != "cancel" && err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
