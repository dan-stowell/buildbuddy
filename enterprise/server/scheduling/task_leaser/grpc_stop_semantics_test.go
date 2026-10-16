package task_leaser_test

// Checks the gRPC behavior that TaskLease relies on when a lease stream is
// broken by a server shutdown. After the server hard-stops a bidi stream (grpc.Server.Stop, reached here through
// the grpc_server.GRPCShutdown path the app uses: GracefulStop, then Stop
// 100ms before the deadline), what does ClientStream.Send return, and what
// does Recv return?

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testgrpc"
	"github.com/buildbuddy-io/buildbuddy/server/util/grpc_client"
	"github.com/buildbuddy-io/buildbuddy/server/util/grpc_server"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	gstatus "google.golang.org/grpc/status"

	scpb "github.com/buildbuddy-io/buildbuddy/proto/scheduler"
)

type echoLeaseServer struct {
	scpb.UnimplementedSchedulerServer
}

func (s *echoLeaseServer) LeaseTask(stream scpb.Scheduler_LeaseTaskServer) error {
	for {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		if err := stream.Send(&scpb.LeaseTaskResponse{LeaseDurationSeconds: 10}); err != nil {
			return err
		}
	}
}

func startServer(t *testing.T) (*grpc.Server, string) {
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	s := grpc.NewServer()
	scpb.RegisterSchedulerServer(s, &echoLeaseServer{})
	go s.Serve(lis)
	return s, "grpc://" + lis.Addr().String()
}

func testHardStop(t *testing.T, viaProxy bool) {
	server, target := startServer(t)
	if viaProxy {
		p := testgrpc.StartProxy(t)
		backend := target
		p.SetDirector(func(ctx context.Context, _ string) (context.Context, *grpc.ClientConn, error) {
			conn, err := grpc_client.DialSimpleWithoutPooling(backend)
			if err != nil {
				return nil, nil, err
			}
			if md, ok := metadata.FromIncomingContext(ctx); ok {
				ctx = metadata.NewOutgoingContext(ctx, md.Copy())
			}
			return ctx, conn, nil
		})
		target = p.GRPCTarget()
	}
	conn, err := grpc_client.DialSimple(target)
	require.NoError(t, err)
	defer conn.Close()
	client := scpb.NewSchedulerClient(conn)

	stream, err := client.LeaseTask(context.Background())
	require.NoError(t, err)
	req := &scpb.LeaseTaskRequest{TaskId: "t", ExecutorId: "e"}
	require.NoError(t, stream.Send(req))
	_, err = stream.Recv()
	require.NoError(t, err)

	// The app's shutdown sequence for the gRPC server: GracefulStop, hard
	// Stop 100ms before the shutdown ctx deadline (the stream never ends on
	// its own, so GracefulStop blocks until the hard stop).
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	require.NoError(t, grpc_server.GRPCShutdown(ctx, server))
	t.Logf("GRPCShutdown returned after %s", time.Since(start))
	time.Sleep(200 * time.Millisecond)

	sendErr := stream.Send(req)
	t.Logf("Send after hard stop: err=%v (==io.EOF: %t, status code: %s)", sendErr, sendErr == io.EOF, gstatus.Code(sendErr))
	_, recvErr := stream.Recv()
	t.Logf("Recv after hard stop: err=%v (status code: %s, IsUnavailable: %t)", recvErr, gstatus.Code(recvErr), status.IsUnavailableError(recvErr))

	require.Equal(t, io.EOF, sendErr, "expected Send to return plain io.EOF")
	require.False(t, status.IsUnavailableError(sendErr) || status.IsInternalError(sendErr),
		"Send error is not Unavailable/Internal, so TaskLease.pingServer would not reconnect")
	require.True(t, status.IsUnavailableError(recvErr), "expected Recv to return Unavailable, got %v", recvErr)
}

func TestSendAfterServerHardStop(t *testing.T) {
	for _, viaProxy := range []bool{false, true} {
		t.Run(fmt.Sprintf("via_proxy=%t", viaProxy), func(t *testing.T) {
			testHardStop(t, viaProxy)
		})
	}
}
