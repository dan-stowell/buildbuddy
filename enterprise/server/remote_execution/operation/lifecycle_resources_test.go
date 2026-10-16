package operation

import (
	"context"
	"fmt"
	"google.golang.org/grpc/stats"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
type diagLog struct {
	mu    sync.Mutex
	start time.Time
	iter  atomic.Int64
	lines []string
}

func (d *diagLog) add(format string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lines = append(d.lines, fmt.Sprintf("[%8.3fms it=%d] ", float64(time.Since(d.start).Microseconds())/1000, d.iter.Load())+fmt.Sprintf(format, args...))
}

type diagListener struct {
	net.Listener
	d *diagLog
}

func (l diagListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.d.add("server ACCEPT remote=%s", c.RemoteAddr())
		return diagConn{Conn: c, d: l.d, side: "server"}, nil
	}
	l.d.add("server accept err=%v", err)
	return c, err
}

type diagConn struct {
	net.Conn
	d    *diagLog
	side string
}

func (c diagConn) Close() error {
	c.d.add("%s conn CLOSE local=%s remote=%s", c.side, c.LocalAddr(), c.RemoteAddr())
	return c.Conn.Close()
}

type diagStats struct {
	d    *diagLog
	side string
}

func (s diagStats) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }
func (s diagStats) HandleRPC(ctx context.Context, rs stats.RPCStats) {
	if e, ok := rs.(*stats.End); ok {
		s.d.add("%s rpc END err=%v", s.side, e.Error)
	}
}
func (s diagStats) TagConn(ctx context.Context, i *stats.ConnTagInfo) context.Context {
	s.d.add("%s conn TAG local=%s remote=%s", s.side, i.LocalAddr, i.RemoteAddr)
	return ctx
}
func (s diagStats) HandleConn(ctx context.Context, cs stats.ConnStats) {
	switch cs.(type) {
	case *stats.ConnBegin:
		s.d.add("%s ConnBegin", s.side)
	case *stats.ConnEnd:
		s.d.add("%s ConnEnd", s.side)
	}
}

func handleStreamsIDs() string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var ids []string
	for _, blk := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(blk, "HandleStreams") || strings.Contains(blk, "http2Client).reader") {
			h, _, _ := strings.Cut(blk, "\n")
			kind := "srv"
			if strings.Contains(blk, "http2Client") {
				kind = "cli"
			}
			ids = append(ids, kind+":"+h)
		}
	}
	return strings.Join(ids, " | ")
}

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
			d := &diagLog{start: time.Now()}
			t.Cleanup(func() {
				if t.Failed() {
					for _, l := range d.lines {
						t.Log("DIAG " + l)
					}
				}
			})
			rawListener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			listener := diagListener{Listener: rawListener, d: d}
			srv := grpc.NewServer(grpc.StatsHandler(diagStats{d: d, side: "server"}))
			repb.RegisterExecutionServer(srv, &resourceExecutionServer{retry: mode == "retry"})
			go srv.Serve(listener)
			t.Cleanup(srv.Stop)
			conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithStatsHandler(diagStats{d: d, side: "client"}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.Close() })
			client := repb.NewExecutionClient(conn)
			testleak.CheckGrowth(t, testleak.Growth{Iterations: 10}, func(i int) {
				d.iter.Store(int64(i))
				d.add("begin; transports: %s", handleStreamsIDs())
				defer func() { d.add("end; state=%s transports: %s", conn.GetState(), handleStreamsIDs()) }()
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
				_, err = pub.CloseAndRecv()
				d.add("CloseAndRecv err=%v", err)
				if mode != "cancel" && err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
