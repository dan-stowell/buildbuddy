package task_leaser_test

import (
	"context"
	"io"
	"net"
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

// leaseServer grants leases and accepts finalize/re-enqueue. With failPing,
// it grants a zero lease duration and fails the next keepalive request.
type leaseServer struct {
	scpb.UnimplementedSchedulerServer
	failPing bool
}

func (s *leaseServer) LeaseTask(stream scpb.Scheduler_LeaseTaskServer) error {
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

// Leasing must not accumulate goroutines (e.g. keepalive loops) or FDs while
// the scheduler connection and server stay up, whether the task succeeds,
// fails, is cancelled, or loses its lease, and nothing may remain once they
// shut down.
func TestLeaseLifecycleResources(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel", "keepalive_failure"} {
		t.Run(mode, func(t *testing.T) {
			testleak.Check(t)
			testleak.CheckFDs(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			srv := grpc.NewServer()
			scpb.RegisterSchedulerServer(srv, &leaseServer{failPing: mode == "keepalive_failure"})
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
			testleak.CheckGrowth(t, testleak.Growth{Iterations: 10}, func(int) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				lease, err := leaser.Lease(ctx, "task")
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "cancel":
					cancel()
				case "keepalive_failure":
					select {
					case <-lease.Context().Done():
					case <-time.After(3 * time.Second):
						t.Fatal("keepalive did not cancel task")
					}
				}
				var taskErr error
				if mode != "success" {
					taskErr = status.Error(codes.Aborted, "task ended")
				}
				lease.Close(ctx, taskErr, mode == "failure")
			})
		})
	}
}
