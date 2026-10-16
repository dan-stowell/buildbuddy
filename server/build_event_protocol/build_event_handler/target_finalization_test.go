package build_event_handler_test

import (
	"context"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_handler"
	"github.com/buildbuddy-io/buildbuddy/server/buildbuddy_server"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testclickhouse"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/authutil"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cmpb "github.com/buildbuddy-io/buildbuddy/proto/api/v1/common"
	bspb "github.com/buildbuddy-io/buildbuddy/proto/build_event_stream"
	inpb "github.com/buildbuddy-io/buildbuddy/proto/invocation"
	trpb "github.com/buildbuddy-io/buildbuddy/proto/target"
)

func TestFinalizedTargetAPIMatchesStorage(t *testing.T) {
	for _, useOLAP := range []bool{false, true} {
		name := "primary"
		if useOLAP {
			name = "olap"
		}
		t.Run(name, func(t *testing.T) {
			flags.Set(t, "app.enable_target_tracking", true)
			flags.Set(t, "app.enable_write_test_target_statuses_to_olap_db", useOLAP)
			te := testenv.GetTestEnv(t)
			if useOLAP {
				testclickhouse.Configure(t, te)
			}
			auth := testauth.NewTestAuthenticator(t, testauth.TestUsers("USER1", "GROUP1"))
			te.SetAuthenticator(auth)
			ctx := auth.AuthContextFromAPIKey(context.Background(), "USER1")
			seq := NewBESSequence(t)
			ch, err := build_event_handler.NewBuildEventHandler(te).OpenChannel(ctx, seq.InvocationID)
			require.NoError(t, err)
			defer ch.Close()
			send := func(e *bspb.BuildEvent) { require.NoError(t, ch.HandleEvent(seq.NextRequest(e))) }
			start := &bspb.BuildEvent{}
			require.NoError(t, startedEvent("--remote_header='"+authutil.APIKeyHeader+"=USER1'").UnmarshalTo(start))
			start.GetStarted().Command = "test"
			start.GetStarted().StartTimeMillis = 1700000000000
			send(start)
			metadata := &bspb.BuildEvent{}
			require.NoError(t, buildMetadataEvent(map[string]string{"ROLE": "CI", "REPO_URL": "https://example.com/test/repo", "COMMIT_SHA": "abc123"}).UnmarshalTo(metadata))
			send(metadata)
			labels := []string{"//app:flaky_test", "//app:cached_test", "//app:aborted_test"}
			configID := func(label string) *bspb.BuildEventId {
				return &bspb.BuildEventId{Id: &bspb.BuildEventId_TargetConfigured{TargetConfigured: &bspb.BuildEventId_TargetConfiguredId{Label: label}}}
			}
			completedID := func(label string) *bspb.BuildEventId {
				return &bspb.BuildEventId{Id: &bspb.BuildEventId_TargetCompleted{TargetCompleted: &bspb.BuildEventId_TargetCompletedId{Label: label}}}
			}
			expanded := &bspb.BuildEvent{Payload: &bspb.BuildEvent_Expanded{Expanded: &bspb.PatternExpanded{}}}
			for _, label := range labels {
				expanded.Children = append(expanded.Children, configID(label))
			}
			send(expanded)
			for _, label := range labels {
				send(&bspb.BuildEvent{Id: configID(label), Payload: &bspb.BuildEvent_Configured{Configured: &bspb.TargetConfigured{TargetKind: "go_test rule", TestSize: bspb.TestSize_SMALL}}})
			}
			workspace := &bspb.BuildEvent{}
			require.NoError(t, workspaceStatusEvent("COMMIT_SHA", "abc123").UnmarshalTo(workspace))
			send(workspace)
			for _, label := range labels[:2] {
				send(&bspb.BuildEvent{Id: completedID(label), Payload: &bspb.BuildEvent_Completed{Completed: &bspb.TargetComplete{Success: true}}})
			}
			send(&bspb.BuildEvent{Id: completedID(labels[2]), Payload: &bspb.BuildEvent_Aborted{Aborted: &bspb.Aborted{Reason: bspb.Aborted_USER_INTERRUPTED}}})
			for _, pair := range [][2]int32{{0, 1}, {0, 2}, {1, 1}} {
				status := bspb.TestStatus_PASSED
				if pair == [2]int32{0, 1} {
					status = bspb.TestStatus_FAILED
				}
				send(&bspb.BuildEvent{Id: &bspb.BuildEventId{Id: &bspb.BuildEventId_TestResult{TestResult: &bspb.BuildEventId_TestResultId{Label: labels[0], Shard: pair[0], Attempt: pair[1]}}}, Payload: &bspb.BuildEvent_TestResult{TestResult: &bspb.TestResult{Status: status}}})
			}
			send(&bspb.BuildEvent{Id: &bspb.BuildEventId{Id: &bspb.BuildEventId_TestResult{TestResult: &bspb.BuildEventId_TestResultId{Label: labels[1], Attempt: 1}}}, Payload: &bspb.BuildEvent_TestResult{TestResult: &bspb.TestResult{Status: bspb.TestStatus_PASSED, CachedLocally: true}}})
			for i, label := range labels[:2] {
				summary := &bspb.TestSummary{OverallStatus: bspb.TestStatus_FLAKY, TotalRunCount: 3}
				if i == 1 {
					summary.OverallStatus = bspb.TestStatus_PASSED
					summary.TotalRunCount = 1
					summary.TotalNumCached = 1
				}
				send(&bspb.BuildEvent{Id: &bspb.BuildEventId{Id: &bspb.BuildEventId_TestSummary{TestSummary: &bspb.BuildEventId_TestSummaryId{Label: label}}}, Payload: &bspb.BuildEvent_TestSummary{TestSummary: summary}})
			}
			finish := &bspb.BuildEvent{}
			require.NoError(t, finishedEvent().UnmarshalTo(finish))
			finish.GetFinished().FinishTimeMillis = 1700000005000
			send(finish)
			send(&bspb.BuildEvent{LastMessage: true})
			require.NoError(t, ch.FinalizeInvocation(seq.InvocationID))
			api, err := buildbuddy_server.NewBuildBuddyServer(te, nil)
			require.NoError(t, err)
			invocation, err := api.GetInvocation(ctx, &inpb.GetInvocationRequest{Lookup: &inpb.InvocationLookup{InvocationId: seq.InvocationID}})
			require.NoError(t, err)
			require.Len(t, invocation.Invocation, 1)
			assert.Equal(t, int64(3), invocation.Invocation[0].TargetConfiguredCount)
			for i, label := range labels[:2] {
				response, err := api.GetTarget(ctx, &trpb.GetTargetRequest{InvocationId: seq.InvocationID, TargetLabel: label})
				require.NoError(t, err)
				require.Len(t, response.TargetGroups, 1)
				group := response.TargetGroups[0]
				assert.Equal(t, int64(1), group.TotalCount)
				require.Len(t, group.Targets, 1)
				assert.Equal(t, []cmpb.Status{cmpb.Status_FLAKY, cmpb.Status_PASSED}[i], group.Targets[0].Status)
				assert.Len(t, group.Targets[0].TestResultEvents, []int{3, 1}[i])
			}
			type stored struct {
				Label  string
				Status int32
				Cached bool
			}
			var rows []stored
			if useOLAP {
				require.NoError(t, te.GetOLAPDBHandle().NewQuery(ctx, "test_target_rows").Raw(`SELECT label, status, cached FROM "TestTargetStatuses"`).Take(&rows))
			} else {
				require.NoError(t, te.GetDBHandle().NewQuery(ctx, "test_target_rows").Raw(`SELECT t.label, ts.status, ts.cached FROM "Targets" t JOIN "TargetStatuses" ts ON ts.target_id = t.target_id`).Take(&rows))
			}
			assert.ElementsMatch(t, []stored{{labels[0], int32(bspb.TestStatus_FLAKY), false}, {labels[1], int32(bspb.TestStatus_PASSED), true}, {labels[2], int32(bspb.TestStatus_TOOL_HALTED_BEFORE_TESTING), false}}, rows)
		})
	}
}
