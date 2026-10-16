package task_leaser_test

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/scheduling/task_leaser"
	scpb "github.com/buildbuddy-io/buildbuddy/proto/scheduler"
	"github.com/buildbuddy-io/buildbuddy/server/real_environment"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type leaseServer struct {
	scpb.UnimplementedSchedulerServer
	active   atomic.Int64
	failPing bool
}

func (s *leaseServer) LeaseTask(stream scpb.Scheduler_LeaseTaskServer) error {
	s.active.Add(1)
	defer s.active.Add(-1)
	first := true
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if req.GetFinalize() || req.GetReEnqueue() {
			if err := stream.Send(&scpb.LeaseTaskResponse{ClosedCleanly: true}); err != nil {
				return err
			}
			return nil
		}
		if !first && s.failPing {
			return status.Error(codes.Aborted, "injected keepalive failure")
		}
		first = false
		ttl := int64(100)
		if s.failPing {
			ttl = 0
		}
		if err := stream.Send(&scpb.LeaseTaskResponse{LeaseDurationSeconds: ttl}); err != nil {
			return err
		}
	}
}
func (s *leaseServer) ReEnqueueTask(context.Context, *scpb.ReEnqueueTaskRequest) (*scpb.ReEnqueueTaskResponse, error) {
	return &scpb.ReEnqueueTaskResponse{}, nil
}
func TestLeaseLifecycleResources(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel", "keepalive_failure"} {
		{
			t.Run(mode, func(t *testing.T) {
				testleak.Check(t)
				testleak.CheckFDs(t)
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				srv := grpc.NewServer()
				handler := &leaseServer{failPing: mode == "keepalive_failure"}
				scpb.RegisterSchedulerServer(srv, handler)
				go srv.Serve(listener)
				t.Cleanup(srv.Stop)
				conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.Close() })
				env := real_environment.NewRealEnv(nil)
				env.SetSchedulerClient(scpb.NewSchedulerClient(conn))
				leaser := task_leaser.NewTaskLeaser(env, "executor", "host", false)
				for range 10 {
					ctx, cancel := context.WithCancel(context.Background())
					lease, err := leaser.Lease(ctx, "task")
					if err != nil {
						cancel()
						t.Fatal(err)
					}
					switch mode {
					case "cancel":
						cancel()
					case "keepalive_failure":
						select {
						case <-lease.Context().Done():
						case <-time.After(3 * time.Second):
							cancel()
							t.Fatal("keepalive did not cancel task")
						}
					}
					var taskErr error
					if mode != "success" {
						taskErr = status.Error(codes.Aborted, "task ended")
					}
					lease.Close(ctx, taskErr, mode == "failure")
					cancel()
				}
			})
		}
	}
}
