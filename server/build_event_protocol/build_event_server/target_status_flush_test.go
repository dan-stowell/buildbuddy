package build_event_server_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_handler"
	"github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_server"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testclickhouse"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/authutil"
	"github.com/buildbuddy-io/buildbuddy/server/util/db"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/buildbuddy-io/buildbuddy/server/util/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"

	bspb "github.com/buildbuddy-io/buildbuddy/proto/build_event_stream"
	bepb "github.com/buildbuddy-io/buildbuddy/proto/build_events"
	inspb "github.com/buildbuddy-io/buildbuddy/proto/invocation_status"
	pepb "github.com/buildbuddy-io/buildbuddy/proto/publish_build_event"
)

// streamEnding describes how the client ends the stream after sending
// BuildFinished.
type streamEnding int

const (
	// Send BuildMetrics + BuildToolLogs (last_message=true), then half-close.
	endWithLastMessage streamEnding = iota
	// Half-close right after BuildFinished (no last_message).
	endWithEOFAfterFinished
	// Cancel the RPC right after BuildFinished (no last_message).
	endWithCancelAfterFinished
)

func (e streamEnding) String() string {
	switch e {
	case endWithLastMessage:
		return "last_message_then_eof"
	case endWithEOFAfterFinished:
		return "eof_after_build_finished"
	case endWithCancelAfterFinished:
		return "cancel_after_build_finished"
	}
	return "unknown"
}

func tfConfiguredID(label string) *bspb.BuildEventId {
	return &bspb.BuildEventId{Id: &bspb.BuildEventId_TargetConfigured{TargetConfigured: &bspb.BuildEventId_TargetConfiguredId{Label: label}}}
}

func tfCompletedID(label string) *bspb.BuildEventId {
	return &bspb.BuildEventId{Id: &bspb.BuildEventId_TargetCompleted{TargetCompleted: &bspb.BuildEventId_TargetCompletedId{Label: label}}}
}

func tfTestResultID(label string) *bspb.BuildEventId {
	return &bspb.BuildEventId{Id: &bspb.BuildEventId_TestResult{TestResult: &bspb.BuildEventId_TestResultId{Label: label, Run: 1, Shard: 1, Attempt: 1}}}
}

func tfTestSummaryID(label string) *bspb.BuildEventId {
	return &bspb.BuildEventId{Id: &bspb.BuildEventId_TestSummary{TestSummary: &bspb.BuildEventId_TestSummaryId{Label: label}}}
}

// buildEvents returns a synthetic `bazel test` BEP stream for two test
// targets, in the order Bazel emits them, up to and including BuildFinished.
func tfEventsThroughFinished() []*bspb.BuildEvent {
	labels := []string{"//pkg:pass_test", "//pkg:fail_test"}
	var events []*bspb.BuildEvent
	events = append(events, &bspb.BuildEvent{
		Id: &bspb.BuildEventId{Id: &bspb.BuildEventId_Started{}},
		Children: []*bspb.BuildEventId{
			{Id: &bspb.BuildEventId_BuildMetadata{}},
			{Id: &bspb.BuildEventId_Pattern{Pattern: &bspb.BuildEventId_PatternExpandedId{Pattern: []string{"//pkg:all"}}}},
			{Id: &bspb.BuildEventId_WorkspaceStatus{}},
			{Id: &bspb.BuildEventId_BuildFinished{}},
		},
		Payload: &bspb.BuildEvent_Started{Started: &bspb.BuildStarted{
			Command:            "test",
			OptionsDescription: "--remote_header='" + authutil.APIKeyHeader + "=USER1'",
		}},
	})
	events = append(events, &bspb.BuildEvent{
		Id: &bspb.BuildEventId{Id: &bspb.BuildEventId_BuildMetadata{}},
		Payload: &bspb.BuildEvent_BuildMetadata{BuildMetadata: &bspb.BuildMetadata{Metadata: map[string]string{
			"ROLE":       "CI",
			"REPO_URL":   "https://github.com/example/repo",
			"COMMIT_SHA": "0123456789abcdef0123456789abcdef01234567",
		}}},
	})
	expanded := &bspb.BuildEvent{
		Id:      &bspb.BuildEventId{Id: &bspb.BuildEventId_Pattern{Pattern: &bspb.BuildEventId_PatternExpandedId{Pattern: []string{"//pkg:all"}}}},
		Payload: &bspb.BuildEvent_Expanded{Expanded: &bspb.PatternExpanded{}},
	}
	for _, l := range labels {
		expanded.Children = append(expanded.Children, tfConfiguredID(l))
	}
	events = append(events, expanded)
	for _, l := range labels {
		events = append(events, &bspb.BuildEvent{
			Id:       tfConfiguredID(l),
			Children: []*bspb.BuildEventId{tfCompletedID(l)},
			Payload: &bspb.BuildEvent_Configured{Configured: &bspb.TargetConfigured{
				TargetKind: "go_test rule",
				TestSize:   bspb.TestSize_SMALL,
			}},
		})
	}
	events = append(events, &bspb.BuildEvent{
		Id:      &bspb.BuildEventId{Id: &bspb.BuildEventId_WorkspaceStatus{}},
		Payload: &bspb.BuildEvent_WorkspaceStatus{WorkspaceStatus: &bspb.WorkspaceStatus{}},
	})
	for i, l := range labels {
		st := bspb.TestStatus_PASSED
		if i == 1 {
			st = bspb.TestStatus_FAILED
		}
		events = append(events,
			&bspb.BuildEvent{
				Id:       tfCompletedID(l),
				Children: []*bspb.BuildEventId{tfTestResultID(l), tfTestSummaryID(l)},
				Payload:  &bspb.BuildEvent_Completed{Completed: &bspb.TargetComplete{Success: true}},
			},
			&bspb.BuildEvent{
				Id:      tfTestResultID(l),
				Payload: &bspb.BuildEvent_TestResult{TestResult: &bspb.TestResult{Status: st}},
			},
			&bspb.BuildEvent{
				Id:      tfTestSummaryID(l),
				Payload: &bspb.BuildEvent_TestSummary{TestSummary: &bspb.TestSummary{OverallStatus: st}},
			},
		)
	}
	events = append(events, &bspb.BuildEvent{
		Id: &bspb.BuildEventId{Id: &bspb.BuildEventId_BuildFinished{}},
		Children: []*bspb.BuildEventId{
			{Id: &bspb.BuildEventId_BuildMetrics{}},
			{Id: &bspb.BuildEventId_BuildToolLogs{}},
		},
		Payload: &bspb.BuildEvent_Finished{Finished: &bspb.BuildFinished{
			ExitCode: &bspb.BuildFinished_ExitCode{Name: "TESTS_FAILED", Code: 3},
		}},
	})
	return events
}

func tfTrailingEvents() []*bspb.BuildEvent {
	return []*bspb.BuildEvent{
		{
			Id:      &bspb.BuildEventId{Id: &bspb.BuildEventId_BuildMetrics{}},
			Payload: &bspb.BuildEvent_BuildMetrics{BuildMetrics: &bspb.BuildMetrics{}},
		},
		{
			Id:          &bspb.BuildEventId{Id: &bspb.BuildEventId_BuildToolLogs{}},
			Payload:     &bspb.BuildEvent_BuildToolLogs{BuildToolLogs: &bspb.BuildToolLogs{}},
			LastMessage: true,
		},
	}
}

func tfRequest(t *testing.T, iid string, seq int64, ev *bspb.BuildEvent) *pepb.PublishBuildToolEventStreamRequest {
	a := &anypb.Any{}
	require.NoError(t, a.MarshalFrom(ev))
	return &pepb.PublishBuildToolEventStreamRequest{
		OrderedBuildEvent: &pepb.OrderedBuildEvent{
			StreamId:       &bepb.StreamId{InvocationId: iid},
			SequenceNumber: seq,
			Event:          &bepb.BuildEvent{Event: &bepb.BuildEvent_BazelEvent{BazelEvent: a}},
		},
	}
}

type tfStatusRow struct {
	Label  string
	Status int32
}

// Signal only after the handler has processed BuildFinished, so cancellation
// cannot race the delivery of the event being tested.
type tfObservedHandler struct {
	interfaces.BuildEventHandler
	finished chan struct{}
}

func (h *tfObservedHandler) OpenChannel(ctx context.Context, iid string) (interfaces.BuildEventChannel, error) {
	ch, err := h.BuildEventHandler.OpenChannel(ctx, iid)
	return &tfObservedChannel{BuildEventChannel: ch, finished: h.finished}, err
}

type tfObservedChannel struct {
	interfaces.BuildEventChannel
	finished chan struct{}
}

func (c *tfObservedChannel) HandleEvent(req *pepb.PublishBuildToolEventStreamRequest) error {
	if err := c.BuildEventChannel.HandleEvent(req); err != nil {
		return err
	}
	ev := &bspb.BuildEvent{}
	if err := req.GetOrderedBuildEvent().GetEvent().GetBazelEvent().UnmarshalTo(ev); err != nil {
		return err
	}
	if ev.GetFinished() != nil {
		close(c.finished)
	}
	return nil
}

func runTargetStatusFlush(t *testing.T, ending streamEnding, olap bool) {
	te := testenv.GetTestEnv(t)
	flags.Set(t, "app.enable_target_tracking", true)
	flags.Set(t, "app.enable_write_test_target_statuses_to_olap_db", olap)
	if olap {
		testclickhouse.Configure(t, te)
	}
	ta := testauth.NewTestAuthenticator(t, testauth.TestUsers("USER1", "GROUP1"))
	te.SetAuthenticator(ta)
	finished := make(chan struct{})
	te.SetBuildEventHandler(&tfObservedHandler{BuildEventHandler: build_event_handler.NewBuildEventHandler(te), finished: finished})

	server, err := build_event_server.NewBuildEventProtocolServer(te, false /*=synchronous*/)
	require.NoError(t, err)
	grpcServer, runServer, lis := testenv.RegisterLocalGRPCServer(t, te)
	pepb.RegisterPublishBuildEventServer(grpcServer, server)
	go runServer()

	conn, err := testenv.LocalGRPCConn(t, context.Background(), lis)
	require.NoError(t, err)
	client := pepb.NewPublishBuildEventClient(conn)

	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	stream, err := client.PublishBuildToolEventStream(streamCtx)
	require.NoError(t, err)

	iid := uuid.New()
	events := tfEventsThroughFinished()
	if ending == endWithLastMessage {
		events = append(events, tfTrailingEvents()...)
	}
	for i, ev := range events {
		require.NoError(t, stream.Send(tfRequest(t, iid, int64(i+1), ev)))
	}

	switch ending {
	case endWithLastMessage, endWithEOFAfterFinished:
		require.NoError(t, stream.CloseSend())
		for {
			_, err := stream.Recv()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
		}
	case endWithCancelAfterFinished:
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Fatal("BuildFinished was not processed")
		}
		cancelStream()
	}

	authCtx := ta.AuthContextFromAPIKey(context.Background(), "USER1")
	// Wait for finalization (the cancel path finalizes asynchronously).
	var invStatus inspb.InvocationStatus
	require.Eventually(t, func() bool {
		inv, err := build_event_handler.LookupInvocation(te, authCtx, iid)
		if err != nil {
			return false
		}
		invStatus = inv.GetInvocationStatus()
		return invStatus != inspb.InvocationStatus_PARTIAL_INVOCATION_STATUS
	}, 10*time.Second, 50*time.Millisecond, "invocation was never finalized")
	t.Logf("ending=%s olap=%t invocation_status=%s", ending, olap, invStatus)
	require.Equal(t, inspb.InvocationStatus_COMPLETE_INVOCATION_STATUS, invStatus,
		"invocation that sent BuildFinished should be stored as COMPLETE")

	// Allow any asynchronous writes to land.
	time.Sleep(500 * time.Millisecond)

	var rows []tfStatusRow
	if olap {
		q := `SELECT label, status FROM "TestTargetStatuses" WHERE invocation_uuid = ? ORDER BY label`
		err = te.GetOLAPDBHandle().NewQuery(context.Background(), "tf_get_status").Raw(q, strings.ReplaceAll(iid, "-", "")).Take(&rows)
	} else {
		iidBytes, perr := uuid.StringToBytes(iid)
		require.NoError(t, perr)
		q := `SELECT t.label, ts.status FROM "TargetStatuses" AS ts JOIN "Targets" AS t ON t.target_id = ts.target_id WHERE ts.invocation_uuid = ? ORDER BY t.label`
		err = te.GetDBHandle().NewQuery(context.Background(), "tf_get_status").Raw(q, iidBytes).Take(&rows)
	}
	if db.IsRecordNotFound(err) {
		// Take() reports "record not found" when the query matches no rows.
		rows, err = nil, nil
	}
	require.NoError(t, err)
	t.Logf("ending=%s olap=%t stored target statuses: %v", ending, olap, rows)
	require.Equal(t, []tfStatusRow{
		{Label: "//pkg:fail_test", Status: int32(bspb.TestStatus_FAILED)},
		{Label: "//pkg:pass_test", Status: int32(bspb.TestStatus_PASSED)},
	}, rows, "COMPLETE invocation %s (ending=%s) should have its test target statuses stored", iid, ending)
}

func TestTargetStatusFlushOnStreamEnd(t *testing.T) {
	for _, olap := range []bool{false, true} {
		for _, ending := range []streamEnding{endWithLastMessage, endWithEOFAfterFinished, endWithCancelAfterFinished} {
			t.Run(fmt.Sprintf("%s/olap=%t", ending, olap), func(t *testing.T) {
				runTargetStatusFlush(t, ending, olap)
			})
		}
	}
}

// Fail only the final blob flush, after all target events were observed.
type tfFailingBlobstore struct {
	interfaces.Blobstore
	fail bool
}

func (b *tfFailingBlobstore) WriteBlob(ctx context.Context, name string, data []byte) (int, error) {
	if b.fail {
		return 0, status.UnavailableError("test blob flush failure")
	}
	return b.Blobstore.WriteBlob(ctx, name, data)
}

func TestTargetStatusFlushRetry(t *testing.T) {
	for _, olap := range []bool{false, true} {
		for _, scenario := range []string{"blob_failure", "disconnect", "superseded", "complete_replay"} {
			t.Run(fmt.Sprintf("%s/olap=%t", scenario, olap), func(t *testing.T) {
				te := testenv.GetTestEnv(t)
				flags.Set(t, "app.enable_target_tracking", true)
				flags.Set(t, "app.enable_write_test_target_statuses_to_olap_db", olap)
				if olap {
					testclickhouse.Configure(t, te)
				}
				ta := testauth.NewTestAuthenticator(t, testauth.TestUsers("USER1", "GROUP1"))
				te.SetAuthenticator(ta)
				bs := &tfFailingBlobstore{Blobstore: te.GetBlobstore()}
				te.SetBlobstore(bs)
				handler := build_event_handler.NewBuildEventHandler(te)
				iid := uuid.New()
				ctx := ta.AuthContextFromAPIKey(context.Background(), "USER1")
				send := func(finished bool) interfaces.BuildEventChannel {
					ch, err := handler.OpenChannel(ctx, iid)
					require.NoError(t, err)
					t.Cleanup(ch.Close)
					events := tfEventsThroughFinished()
					if !finished {
						events = events[:len(events)-1]
					}
					for i, ev := range events {
						require.NoError(t, ch.HandleEvent(tfRequest(t, iid, int64(i+1), ev)))
					}
					return ch
				}
				count := func(want int64) {
					var row struct{ Count int64 }
					if olap {
						require.NoError(t, te.GetOLAPDBHandle().NewQuery(ctx, "tf_count").Raw(`SELECT count(*) AS count FROM "TestTargetStatuses" WHERE invocation_uuid = ?`, strings.ReplaceAll(iid, "-", "")).Take(&row))
					} else {
						id, err := uuid.StringToBytes(iid)
						require.NoError(t, err)
						require.NoError(t, te.GetDBHandle().NewQuery(ctx, "tf_count").Raw(`SELECT count(*) AS count FROM "TargetStatuses" WHERE invocation_uuid = ?`, id).Take(&row))
					}
					require.Equal(t, want, row.Count)
				}
				first := send(scenario != "disconnect")
				switch scenario {
				case "blob_failure":
					bs.fail = true
					require.Error(t, first.FinalizeInvocation(iid))
					bs.fail = false
				case "disconnect":
					require.NoError(t, first.FinalizeInvocation(iid))
				case "complete_replay":
					require.NoError(t, first.FinalizeInvocation(iid))
					require.NoError(t, first.FinalizeInvocation(iid))
					count(2)
				}
				if scenario != "complete_replay" {
					count(0)
				}
				second := send(true)
				if scenario == "superseded" {
					require.Error(t, first.FinalizeInvocation(iid))
					count(0)
				}
				require.NoError(t, second.FinalizeInvocation(iid))
				require.NoError(t, second.FinalizeInvocation(iid))
				count(2)
				inv, err := te.GetInvocationDB().LookupInvocation(ctx, iid)
				require.NoError(t, err)
				wantAttempt := uint64(2)
				if scenario == "complete_replay" {
					wantAttempt = 1
				}
				require.Equal(t, wantAttempt, inv.Attempt)
			})
		}
	}
}
