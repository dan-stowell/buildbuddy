package build_event_server_test

import (
	"context"
	"fmt"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"google.golang.org/grpc"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_server"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testbes"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bepb "github.com/buildbuddy-io/buildbuddy/proto/build_events"
	pepb "github.com/buildbuddy-io/buildbuddy/proto/publish_build_event"
)

func TestPublishBuildToolEventStream_NoEvents(t *testing.T) {
	env := testenv.GetTestEnv(t)
	env.SetBuildEventHandler(NewTestBuildEventHandler())
	server, err := build_event_server.NewBuildEventProtocolServer(env, false /*=synchronous*/)
	require.NoError(t, err)
	grpcServer, runServer, lis := testenv.RegisterLocalGRPCServer(t, env)
	pepb.RegisterPublishBuildEventServer(grpcServer, server)
	go runServer()

	// Make a PublishBuildToolEventStream RPC but close it without sending
	// anything.
	ctx := context.Background()
	conn, err := testenv.LocalGRPCConn(t, ctx, lis)
	require.NoError(t, err)
	client := pepb.NewPublishBuildEventClient(conn)
	stream, err := client.PublishBuildToolEventStream(ctx)
	require.NoError(t, err)
	err = stream.CloseSend()
	require.NoError(t, err)

	_, err = stream.Recv()
	require.Equal(t, io.EOF, err)
}

type proxyTestCase struct {
	Name            string
	Synchronous     bool
	UpstreamHandler testbes.HandlerFunc
	ExpectedError   error
}

func TestPublishBuildToolEventStream_Proxy(t *testing.T) {
	for _, test := range []proxyTestCase{
		{
			Name:            "can proxy events to upstream in sync mode",
			Synchronous:     true,
			UpstreamHandler: testbes.Ack,
			ExpectedError:   nil,
		},
		{
			Name:            "can proxy events to upstream in async mode",
			Synchronous:     false,
			UpstreamHandler: testbes.Ack,
			ExpectedError:   nil,
		},
		{
			Name:            "should ignore upstream recv errors in async mode",
			Synchronous:     false,
			UpstreamHandler: testbes.FailWith(status.UnavailableErrorf("test error")),
			ExpectedError:   nil,
		},
		{
			Name:            "should return upstream recv errors in sync mode",
			Synchronous:     true,
			UpstreamHandler: testbes.FailWith(status.UnavailableErrorf("test error")),
			ExpectedError:   status.UnavailableError("recv from proxy stream: test error"),
		},
	} {
		t.Run(test.Name, func(t *testing.T) {
			testPublishBuildToolEventStreamProxy(t, test)
		})
	}
}

func testPublishBuildToolEventStreamProxy(t *testing.T, test proxyTestCase) {
	env := testenv.GetTestEnv(t)
	env.SetBuildEventHandler(NewTestBuildEventHandler())

	// Run a test BE server and configure it as the upstream client.
	upstream, upstreamClient := testbes.Run(t)
	upstream.EventHandler = test.UpstreamHandler
	env.SetBuildEventProxyClients([]pepb.PublishBuildEventClient{upstreamClient})

	server, err := build_event_server.NewBuildEventProtocolServer(env, test.Synchronous)
	require.NoError(t, err)
	grpcServer, runServer, lis := testenv.RegisterLocalGRPCServer(t, env)
	pepb.RegisterPublishBuildEventServer(grpcServer, server)
	go runServer()

	ctx := context.Background()
	conn, err := testenv.LocalGRPCConn(t, ctx, lis)
	require.NoError(t, err)
	client := pepb.NewPublishBuildEventClient(conn)
	stream, err := client.PublishBuildToolEventStream(ctx)
	require.NoError(t, err)

	// Publish one message to trigger an error from the upstream server.
	err = stream.Send(&pepb.PublishBuildToolEventStreamRequest{
		OrderedBuildEvent: &pepb.OrderedBuildEvent{
			StreamId:       &bepb.StreamId{InvocationId: uuid.New()},
			SequenceNumber: 1,
		},
	})
	require.NoError(t, err)

	err = stream.CloseSend()
	require.NoError(t, err)

	rsp, err := stream.Recv()
	if test.ExpectedError == nil {
		require.NoError(t, err)
		assert.Equal(t, int64(1), rsp.GetSequenceNumber())
	} else {
		require.Equal(t, test.ExpectedError.Error(), err.Error())
	}
}

type TestBuildEventHandler struct{}

func NewTestBuildEventHandler() *TestBuildEventHandler {
	return &TestBuildEventHandler{}
}

func (h *TestBuildEventHandler) OpenChannel(ctx context.Context, invocationID string) (interfaces.BuildEventChannel, error) {
	return &TestBuildEventChannel{ctx: ctx}, nil
}

type TestBuildEventChannel struct{ ctx context.Context }

func (c *TestBuildEventChannel) HandleEvent(event *pepb.PublishBuildToolEventStreamRequest) error {
	return nil
}
func (c *TestBuildEventChannel) GetNumDroppedEvents() uint64                  { return 0 }
func (c *TestBuildEventChannel) GetInitialSequenceNumber() int64              { return 1 }
func (c *TestBuildEventChannel) Context() context.Context                     { return c.ctx }
func (c *TestBuildEventChannel) FinalizeInvocation(invocationID string) error { return nil }
func (c *TestBuildEventChannel) Close()                                       {}

// Exercise transport-owned Recv goroutines, including server early returns
// while the client has not half-closed its send side.
func TestBESRPCResources(t *testing.T) {
	flags.Set(t, "grpc_server_worker_multiplier", 0)
	for _, outcome := range []string{"empty", "finish", "cancel", "invalid", "open_error", "sequence_gap"} {
		for _, n := range []int{1, 10, 50} {
			t.Run(fmt.Sprintf("%s/N=%d", outcome, n), func(t *testing.T) {
				testleak.Check(t)
				env := testenv.GetTestEnv(t)
				if outcome == "open_error" {
					env.SetBuildEventHandler(&rejectingBESHandler{})
				} else {
					env.SetBuildEventHandler(NewTestBuildEventHandler())
				}
				server, err := build_event_server.NewBuildEventProtocolServer(env, false)
				require.NoError(t, err)
				grpcServer, runServer, lis := testenv.RegisterLocalGRPCServer(t, env)
				pepb.RegisterPublishBuildEventServer(grpcServer, server)
				go runServer()
				conn, err := testenv.LocalGRPCConn(t, context.Background(), lis)
				require.NoError(t, err)
				client := pepb.NewPublishBuildEventClient(conn)
				for range n {
					ctx, cancel := context.WithCancel(context.Background())
					stream, err := client.PublishBuildToolEventStream(ctx)
					require.NoError(t, err)
					if outcome != "empty" {
						iid := uuid.New()
						if outcome == "invalid" {
							iid = ""
						}
						seq := int64(1)
						if outcome == "sequence_gap" {
							seq = 2
						}
						require.NoError(t, stream.Send(&pepb.PublishBuildToolEventStreamRequest{OrderedBuildEvent: &pepb.OrderedBuildEvent{StreamId: &bepb.StreamId{InvocationId: iid}, SequenceNumber: seq}}))
					}
					switch outcome {
					case "cancel":
						cancel()
					case "invalid", "open_error": // Server must return without client EOF.
					default:
						require.NoError(t, stream.CloseSend())
					}
					for {
						_, err = stream.Recv()
						if err != nil {
							break
						}
					}
					if outcome == "empty" || outcome == "finish" {
						require.Equal(t, io.EOF, err)
					} else {
						require.Error(t, err)
						require.NotEqual(t, io.EOF, err)
					}
					cancel()
				}
				require.Eventually(t, func() bool {
					b := make([]byte, 2<<20)
					b = b[:runtime.Stack(b, true)]
					return !strings.Contains(string(b), "build_event_server.(*BuildEventProtocolServer).PublishBuildToolEventStream")
				}, 15*time.Second, 10*time.Millisecond)
				t.Logf("N=%d transport RPCs terminated; goroutines before environment cleanup=%d", n, runtime.NumGoroutine())
			})
		}
	}
}

type rejectingBESHandler struct{}

func (*rejectingBESHandler) OpenChannel(context.Context, string) (interfaces.BuildEventChannel, error) {
	return nil, status.PermissionDeniedError("test auth failure")
}

// TestRevalWorkerProbe is an opt-in detector calibration. With workers=2 the
// deliberately blocked handlers are invisible; workers=0 must fail Check.
// Release happens after measurement, so this probe leaves no actual leak.
func TestRevalWorkerProbe(t *testing.T) {
	mode := os.Getenv("REVAL_WORKERS")
	if mode == "" {
		t.Skip("set REVAL_WORKERS=2 or 0 to calibrate detector")
	}
	workers := 2
	if mode == "0" {
		workers = 0
	}
	flags.Set(t, "grpc_server_worker_multiplier", workers)
	env := testenv.GetTestEnv(t)
	srv, run, lis := testenv.RegisterLocalGRPCServer(t, env)
	releases := make(chan chan struct{}, 8)
	started := make(chan struct{}, 8)
	srv.RegisterService(&grpc.ServiceDesc{ServiceName: "reval.Probe", HandlerType: (*any)(nil), Streams: []grpc.StreamDesc{{StreamName: "Block", ServerStreams: true, ClientStreams: true, Handler: func(_ any, stream grpc.ServerStream) error {
		release := <-releases
		started <- struct{}{}
		<-release
		return nil
	}}}}, struct{}{})
	go run()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := testenv.LocalGRPCConn(t, ctx, lis)
	require.NoError(t, err)
	// Warm the connection before the snapshot. Warmup handlers finish normally.
	var blocked []chan struct{}
	defer func() {
		for _, c := range blocked {
			close(c)
		}
	}()
	op := func(i int) {
		release := make(chan struct{})
		releases <- release
		rpcCtx, rpcCancel := context.WithCancel(ctx)
		stream, err := conn.NewStream(rpcCtx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, "/reval.Probe/Block")
		require.NoError(t, err)
		require.NoError(t, stream.CloseSend())
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("handler did not start")
		}
		if i == 0 {
			close(release)
		} else {
			blocked = append(blocked, release)
			rpcCancel()
		}
		require.Error(t, stream.RecvMsg(new(any)))
		rpcCancel()
	}
	op(0)
	t.Run("snapshot", func(t *testing.T) {
		testleak.Check(t)
		for i := range 3 {
			op(i + 1)
		}
	})
	t.Logf("workers=%d deliberately blocked handlers=%d", workers, len(blocked))
}
