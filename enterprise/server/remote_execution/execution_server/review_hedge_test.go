package execution_server_test

import (
	"context"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/operation"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
)

type reviewPendingScheduler struct{ *schedulerServerMock }

func (*reviewPendingScheduler) ExistsTask(context.Context, string) (bool, error) { return true, nil }

func TestReviewHedgeDelivery(t *testing.T)     { reviewHedgeCompletion(t, true) }
func TestReviewHedgeCancellation(t *testing.T) { reviewHedgeCompletion(t, false) }

// Complete only the hedge. No executor processes or timing-dependent work are
// needed to check delivery and the request to cancel its running sibling.
func reviewHedgeCompletion(t *testing.T, delivery bool) {
	flags.Set(t, "remote_execution.enable_action_merging", true)
	flags.Set(t, "remote_execution.action_merging_hedge_count", 1)
	flags.Set(t, "remote_execution.action_merging_hedge_delay", time.Duration(0))
	env, conn, _ := setupEnv(t)
	scheduler := &reviewPendingScheduler{env.GetSchedulerService().(*schedulerServerMock)}
	env.SetSchedulerService(scheduler)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := repb.NewExecutionClient(conn)
	arn := uploadAction(ctx, t, env, "", repb.DigestFunction_SHA256, &repb.Action{})
	req := &repb.ExecuteRequest{ActionDigest: arn.GetDigest(), DigestFunction: arn.GetDigestFunction()}
	original, err := client.Execute(ctx, req)
	require.NoError(t, err)
	accepted, err := original.Recv()
	require.NoError(t, err)
	merged, err := client.Execute(ctx, req)
	require.NoError(t, err)
	mergedAccepted, err := merged.Recv()
	require.NoError(t, err)
	require.Equal(t, accepted.GetName(), mergedAccepted.GetName())
	require.Len(t, scheduler.scheduleReqs, 2)
	op, err := operation.Assemble(scheduler.scheduleReqs[1].GetTaskId(), operation.Metadata(repb.ExecutionStage_COMPLETED, arn.GetDigest()), &repb.ExecuteResponse{Result: &repb.ActionResult{StdoutRaw: []byte("winner")}})
	require.NoError(t, err)
	publisher, err := client.PublishOperation(ctx)
	require.NoError(t, err)
	require.NoError(t, publisher.Send(op))
	_, err = publisher.CloseAndRecv()
	require.NoError(t, err)
	if !delivery {
		require.Equal(t, 1, scheduler.canceledCount, "hedge completion must cancel the canonical sibling")
		return
	}
	for {
		op, err := original.Recv()
		require.NoError(t, err, "canonical waiter must receive successful hedge")
		require.Equal(t, accepted.GetName(), op.GetName())
		if op.GetDone() {
			require.Equal(t, "winner", string(operation.ExtractExecuteResponse(op).GetResult().GetStdoutRaw()))
			return
		}
	}
}
