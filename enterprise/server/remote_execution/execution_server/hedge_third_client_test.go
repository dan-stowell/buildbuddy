package execution_server_test

import (
	"context"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/operation"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	"slices"
	"testing"
	"time"
)

func TestHedgeWinnerCompletesThirdMergedClient(t *testing.T) {
	flags.Set(t, "remote_execution.enable_action_merging", true)
	flags.Set(t, "remote_execution.action_merging_hedge_count", 1)
	flags.Set(t, "remote_execution.action_merging_hedge_delay", time.Duration(0))
	env, conn, _ := setupEnv(t)
	scheduler := &hedgeSchedulerMock{env.GetSchedulerService().(*schedulerServerMock)}
	scheduler.taskLost = make(chan struct{})
	env.SetSchedulerService(scheduler)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := repb.NewExecutionClient(conn)
	arn := uploadAction(ctx, t, env, "", repb.DigestFunction_SHA256, &repb.Action{})
	req := &repb.ExecuteRequest{ActionDigest: arn.GetDigest(), DigestFunction: arn.GetDigestFunction()}
	var streams []repb.Execution_ExecuteClient
	var canonical string
	for i := range 3 {
		stream, err := client.Execute(ctx, req)
		require.NoError(t, err)
		accepted, err := stream.Recv()
		require.NoError(t, err)
		if i == 0 {
			canonical = accepted.GetName()
		}
		require.Equal(t, canonical, accepted.GetName())
		streams = append(streams, stream)
	}
	require.Len(t, scheduler.scheduleReqs, 2, "third client must merge without a second hedge")
	op, err := operation.Assemble(scheduler.scheduleReqs[1].GetTaskId(),
		operation.Metadata(repb.ExecutionStage_COMPLETED, arn.GetDigest()),
		&repb.ExecuteResponse{Result: &repb.ActionResult{StdoutRaw: []byte("hedge winner")}})
	require.NoError(t, err)
	publisher, err := client.PublishOperation(ctx)
	require.NoError(t, err)
	require.NoError(t, publisher.Send(op))
	_, err = publisher.CloseAndRecv()
	require.NoError(t, err)
	// Read the third client first to isolate delivery to a post-hedge merge.
	for i, stream := range slices.Backward(streams) {
		for {
			op, err := stream.Recv()
			require.NoError(t, err, "client %d must receive the hedge result", i)
			require.Equal(t, canonical, op.GetName())
			if !op.GetDone() {
				continue
			}
			require.Equal(t, "hedge winner", string(operation.ExtractExecuteResponse(op).GetResult().GetStdoutRaw()))
			break
		}
	}

	// A losing canonical executor can still send an in-flight progress update.
	// Reconnects must recover the elected terminal result when cancellation makes
	// the canonical task disappear, even if that progress update is stream-tail.
	late, err := operation.Assemble(canonical, operation.Metadata(repb.ExecutionStage_EXECUTING, arn.GetDigest()), operation.InProgressExecuteResponse())
	require.NoError(t, err)
	publisher, err = client.PublishOperation(ctx)
	require.NoError(t, err)
	require.NoError(t, publisher.Send(late))
	_, err = publisher.CloseAndRecv()
	require.NoError(t, err)
	close(scheduler.taskLost)
	reconnect, err := client.WaitExecution(ctx, &repb.WaitExecutionRequest{Name: canonical})
	require.NoError(t, err)
	for {
		op, err := reconnect.Recv()
		require.NoError(t, err)
		require.Equal(t, canonical, op.GetName())
		if !op.GetDone() {
			continue
		}
		require.Equal(t, "hedge winner", string(operation.ExtractExecuteResponse(op).GetResult().GetStdoutRaw()))
		break
	}
}
