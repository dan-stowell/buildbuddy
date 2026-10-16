package task_leaser_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
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
func sockets(t *testing.T) int {
	t.Helper()
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range es {
		v, _ := os.Readlink("/proc/self/fd/" + e.Name())
		if strings.HasPrefix(v, "socket:[") {
			n++
		}
	}
	return n
}
func settle(check func() bool) bool {
	d := time.Now().Add(3 * time.Second)
	for !check() {
		if time.Now().After(d) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}
func TestLeaseLifecycleResources(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel", "keepalive_failure"} {
		for _, n := range []int{1, 10, 50} {
			t.Run(fmt.Sprintf("%s/N=%d", mode, n), func(t *testing.T) {
				testleak.Check(t)
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				// Intentionally omit BuildBuddy CommonGRPCServerOptions: this server
				// has no pre-spawned worker pool to hide stuck handlers from testleak.
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
				// Warm the shared connection; its bounded transport goroutines are owned by conn.
				warmCtx, warmCancel := context.WithCancel(context.Background())
				warm, err := leaser.Lease(warmCtx, "warm")
				if err != nil {
					t.Fatal(err)
				}
				warm.Close(warmCtx, nil, false)
				warmCancel()
				if !settle(func() bool { return handler.active.Load() == 0 }) {
					t.Fatal("warm lease did not finish")
				}
				beforeFD, beforeG := sockets(t), runtime.NumGoroutine()
				for range n {
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
				ok := settle(func() bool {
					return handler.active.Load() == 0 && sockets(t) <= beforeFD && runtime.NumGoroutine() <= beforeG
				})
				t.Logf("N=%d mode=%s active_streams=%d sockets=%d->%d goroutines=%d->%d", n, mode, handler.active.Load(), beforeFD, sockets(t), beforeG, runtime.NumGoroutine())
				if !ok {
					t.Error("per-task resources did not settle")
				}
			})
		}
	}
}
