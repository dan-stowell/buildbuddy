package build_event_handler_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_handler"
	"github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_server"
	"github.com/buildbuddy-io/buildbuddy/server/tables"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testolapdb"
	"github.com/buildbuddy-io/buildbuddy/server/util/authutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"

	bspb "github.com/buildbuddy-io/buildbuddy/proto/build_event_stream"
	inspb "github.com/buildbuddy-io/buildbuddy/proto/invocation_status"
	pepb "github.com/buildbuddy-io/buildbuddy/proto/publish_build_event"
)

// Drive the real RPC implementation with a finite, deterministic receive stream.
// Embedding supplies unused grpc.ServerStream methods.
type finalizationStream struct {
	pepb.PublishBuildEvent_PublishBuildToolEventStreamServer
	ctx      context.Context
	requests []*pepb.PublishBuildToolEventStreamRequest
	terminal error
	cancel   context.CancelFunc
	acks     []int64
}

func (s *finalizationStream) Context() context.Context { return s.ctx }
func (s *finalizationStream) Recv() (*pepb.PublishBuildToolEventStreamRequest, error) {
	if len(s.requests) == 0 {
		if s.cancel != nil {
			s.cancel()
		}
		return nil, s.terminal
	}
	r := s.requests[0]
	s.requests = s.requests[1:]
	return r, nil
}
func (s *finalizationStream) Send(r *pepb.PublishBuildToolEventStreamResponse) error {
	s.acks = append(s.acks, r.GetSequenceNumber())
	return nil
}

func checkStreamFinalization(t *testing.T, order []int, terminal error, cancelContext bool, exitCode int32) {
	t.Helper()
	te := testenv.GetTestEnv(t)
	auth := testauth.NewTestAuthenticator(t, testauth.TestUsers("USER1", "GROUP1"))
	te.SetAuthenticator(auth)
	olap := testolapdb.NewHandle()
	te.SetOLAPDBHandle(olap)
	te.SetBuildEventHandler(build_event_handler.NewBuildEventHandler(te))
	server, err := build_event_server.NewBuildEventProtocolServer(te, false)
	require.NoError(t, err)
	const iid = "00000000-0000-0000-0000-000000000123"
	started := &bspb.BuildEvent{}
	require.NoError(t, startedEvent("--remote_header='"+authutil.APIKeyHeader+"=USER1'", &bspb.BuildEventId_Pattern{Pattern: &bspb.BuildEventId_PatternExpandedId{Pattern: []string{"//app:tests"}}}).UnmarshalTo(started))
	started.GetStarted().StartTimeMillis = 1700000000000
	started.GetStarted().Command = "test"
	finished := &bspb.BuildEvent{}
	require.NoError(t, finishedEvent().UnmarshalTo(finished))
	finished.GetFinished().FinishTimeMillis = 1700000002500
	finished.GetFinished().ExitCode.Code = exitCode
	finished.GetFinished().ExitCode.Name = "SUCCESS"
	if exitCode != 0 {
		finished.GetFinished().ExitCode.Name = "TESTS_FAILED"
	}
	startAny, err := anypb.New(started)
	require.NoError(t, err)
	finishAny, err := anypb.New(finished)
	require.NoError(t, err)
	logsAny, err := anypb.New(&bspb.BuildEvent{LastMessage: true, Payload: &bspb.BuildEvent_BuildToolLogs{BuildToolLogs: &bspb.BuildToolLogs{}}})
	require.NoError(t, err)
	events := []*anypb.Any{startAny, workspaceStatusEvent("COMMIT_SHA", "abc123"), finishAny, logsAny}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &finalizationStream{ctx: ctx, terminal: terminal}
	if cancelContext {
		stream.cancel = cancel
	}
	finishedSeen := false
	for _, i := range order {
		stream.requests = append(stream.requests, streamRequest(events[i-1], iid, int64(i)))
		finishedSeen = finishedSeen || i == 3
	}
	err = server.PublishBuildToolEventStream(stream)
	if terminal == io.EOF && len(order) > 0 && !cancelContext {
		// Sequence validation may reject a gap or duplicate, but must still finalize.
		t.Logf("RPC result: %v; ACKs: %v", err, stream.acks)
	} else {
		require.Error(t, err)
	}
	readCtx := auth.AuthContextFromAPIKey(context.Background(), "USER1")
	row, err := te.GetInvocationDB().LookupInvocation(readCtx, iid)
	require.NoError(t, err)
	wantStatus := inspb.InvocationStatus_DISCONNECTED_INVOCATION_STATUS
	var wantDuration int64
	wantExit := ""
	if finishedSeen {
		wantStatus = inspb.InvocationStatus_COMPLETE_INVOCATION_STATUS
		wantDuration = 2500000
		wantExit = finished.GetFinished().ExitCode.Name
	}
	assert.Equal(t, int64(wantStatus), row.InvocationStatus, "primary DB status")
	assert.Equal(t, finishedSeen && exitCode == 0, row.Success)
	assert.Equal(t, wantDuration, row.DurationUsec)
	assert.Equal(t, "test", row.Command)
	assert.Equal(t, "//app:tests", row.Pattern)
	assert.Equal(t, wantExit, row.BazelExitCode)
	// Do not wait for an async write when the synchronous finalization already failed.
	if row.InvocationStatus != int64(wantStatus) {
		return
	}
	inv, err := build_event_handler.LookupInvocation(te, readCtx, iid)
	require.NoError(t, err)
	assert.Equal(t, wantStatus, inv.InvocationStatus, "API lookup status")
	assert.Equal(t, row.Success, inv.Success)
	assert.Equal(t, row.DurationUsec, inv.DurationUsec)
	assert.Equal(t, row.Command, inv.Command)
	assert.Equal(t, []string{row.Pattern}, inv.Pattern)
	assert.Equal(t, row.BazelExitCode, inv.BazelExitCode)
	var analytics *tables.Invocation
	require.Eventually(t, func() bool { analytics = olap.GetFlushedInvocation(iid); return analytics != nil }, 30*time.Second, 10*time.Millisecond)
	assert.Equal(t, row.InvocationStatus, analytics.InvocationStatus)
	assert.Equal(t, row.Success, analytics.Success)
	assert.Equal(t, row.DurationUsec, analytics.DurationUsec)
	assert.Equal(t, row.Command, analytics.Command)
	assert.Equal(t, row.Pattern, analytics.Pattern)
	assert.Equal(t, row.BazelExitCode, analytics.BazelExitCode)
}

func TestStreamFinalizationSummary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		order    []int
		terminal error
		cancel   bool
		exit     int32
	}{
		{"normal", []int{1, 2, 3, 4}, io.EOF, false, 0},
		{"failed_build", []int{1, 2, 3, 4}, io.EOF, false, 3},
		{"without_finished", []int{1, 2}, io.EOF, false, 0},
		{"finished_without_logs", []int{1, 2, 3}, io.EOF, false, 0},
		{"client_cancelled", []int{1, 2}, context.Canceled, true, 0},
		{"client_timeout", []int{1, 2}, context.DeadlineExceeded, false, 0},
		{"cancelled_after_finished", []int{1, 2, 3}, context.Canceled, true, 0},
		{"reordered_metadata", []int{1, 3, 2, 4}, io.EOF, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) { checkStreamFinalization(t, tc.order, tc.terminal, tc.cancel, tc.exit) })
	}
}
