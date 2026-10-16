package operation

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
func resourceSockets(t *testing.T) int {
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
func resourceSettle(check func() bool) bool {
	d := time.Now().Add(3 * time.Second)
	for !check() {
		if time.Now().After(d) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}
func TestPublisherLifecycleResources(t *testing.T) {
	for _, mode := range []string{"success", "cancel", "retry"} {
		for _, n := range []int{1, 10, 50} {
			t.Run(fmt.Sprintf("%s/N=%d", mode, n), func(t *testing.T) {
				testleak.Check(t)
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
				execute(false)
				if !resourceSettle(func() bool { return handler.active.Load() == 0 }) {
					t.Fatal("warm stream still active")
				}
				beforeFD, beforeG := resourceSockets(t), runtime.NumGoroutine()
				for range n {
					execute(mode == "cancel")
				}
				ok := resourceSettle(func() bool {
					return handler.active.Load() == 0 && resourceSockets(t) <= beforeFD && runtime.NumGoroutine() <= beforeG
				})
				t.Logf("N=%d mode=%s active_streams=%d sockets=%d->%d goroutines=%d->%d", n, mode, handler.active.Load(), beforeFD, resourceSockets(t), beforeG, runtime.NumGoroutine())
				if !ok {
					t.Error("publisher resources did not settle")
				}
			})
		}
	}
}
