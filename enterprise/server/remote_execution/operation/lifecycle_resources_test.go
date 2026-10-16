package operation

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type resourceExecutionServer struct {
	repb.UnimplementedExecutionServer
	active atomic.Int64
	opened atomic.Int64
	retry  bool
}

func (s *resourceExecutionServer) PublishOperation(stream repb.Execution_PublishOperationServer) error {
	s.active.Add(1)
	defer s.active.Add(-1)
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
func TestPublisherLifecycleResources(t *testing.T) {
	for _, mode := range []string{"success", "cancel", "retry"} {
		{
			t.Run(mode, func(t *testing.T) {
				testleak.Check(t)
				testleak.CheckFDs(t)
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				srv := grpc.NewServer()
				handler := &resourceExecutionServer{retry: mode == "retry"}
				repb.RegisterExecutionServer(srv, handler)
				go srv.Serve(listener)
				t.Cleanup(srv.Stop)
				conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.Close() })
				client := repb.NewExecutionClient(conn)
				execute := func(cancelEarly bool) {
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
					if cancelEarly {
						cancel()
					}
					_, err = pub.CloseAndRecv()
					if !cancelEarly && err != nil {
						t.Fatal(err)
					}
				}
				for range 10 {
					execute(mode == "cancel")
				}
			})
		}
	}
}
