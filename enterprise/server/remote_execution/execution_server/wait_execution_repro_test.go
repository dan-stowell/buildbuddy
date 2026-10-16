package execution_server_test

import (
	"context"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/operation"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/cachetools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	gstatus "google.golang.org/grpc/status"
)

// waitTimeout is the client-side deadline used to detect a WaitExecution that
// never ends. Bazel's default executor client sets no deadline at all, so a
// WaitExecution that is still open after this long would stay open forever.
const waitTimeout = 5 * time.Second

type waitOutcome struct {
	ops     []*longrunningpb.Operation
	err     error
	elapsed time.Duration
}

func callWaitExecution(ctx context.Context, t *testing.T, conn *grpc.ClientConn, name string) waitOutcome {
	ctx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	start := time.Now()
	stream, err := repb.NewExecutionClient(conn).WaitExecution(ctx, &repb.WaitExecutionRequest{Name: name})
	require.NoError(t, err)
	var out waitOutcome
	for {
		op, err := stream.Recv()
		if err != nil {
			out.err = err
			break
		}
		out.ops = append(out.ops, op)
		if op.GetDone() {
			break
		}
	}
	out.elapsed = time.Since(start)
	t.Logf("WaitExecution(%q): %d ops, err=%v, elapsed=%s", name, len(out.ops), out.err, out.elapsed)
	for i, op := range out.ops {
		t.Logf("  op[%d]: done=%t stage=%s status=%v", i, op.GetDone(), operation.ExtractStage(op), operation.ExtractExecuteResponse(op).GetStatus())
	}
	return out
}

// requireWaitEnded fails if the server left the WaitExecution open until the
// client's deadline instead of ending it (with a final operation or an error
// such as NOT_FOUND that makes Bazel retry Execute).
func requireWaitEnded(t *testing.T, out waitOutcome) {
	if gstatus.Code(out.err) == codes.DeadlineExceeded {
		t.Fatalf("server never ended WaitExecution: still open at the client's %s deadline (ops received: %d)", waitTimeout, len(out.ops))
	}
}

func setupWaitTest(t *testing.T, monitoring bool) (*testenv.TestEnv, *grpc.ClientConn, context.Context) {
	flags.Set(t, "remote_execution.enable_redis_availability_monitoring", monitoring)
	env, conn, _ := setupEnv(t)
	// With remote_execution.task_liveness_check_interval=0 (the default), the
	// scheduler's WatchTaskLiveness returns a nil channel.
	env.GetSchedulerService().(*schedulerServerMock).taskLost = nil
	ta := testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1"))
	env.SetAuthenticator(ta)
	ctx, err := ta.WithAuthenticatedUser(context.Background(), "US1")
	require.NoError(t, err)
	return env, conn, ctx
}

func monitoringCases() []struct {
	name       string
	monitoring bool
} {
	return []struct {
		name       string
		monitoring bool
	}{
		{"RedisAvailabilityMonitoringOff", false},
		{"RedisAvailabilityMonitoringOn", true},
	}
}

// Claim A, case 1: WaitExecution for a well-formed operation name that was
// never dispatched (or whose state is entirely gone from Redis).
func TestWaitExecutionRepro_UnknownOperation(t *testing.T) {
	for _, tc := range monitoringCases() {
		t.Run(tc.name, func(t *testing.T) {
			env, conn, ctx := setupWaitTest(t, tc.monitoring)
			arn := uploadAction(ctx, t, env, "", repb.DigestFunction_SHA256, &repb.Action{})
			name := arn.NewUploadString()
			out := callWaitExecution(ctx, t, conn, name)
			requireWaitEnded(t, out)
		})
	}
}

// startExecutionAndDisconnect calls Execute, reads the initial update and then
// drops the Execute stream, as Bazel's stream does when its connection breaks.
func startExecutionAndDisconnect(ctx context.Context, t *testing.T, env *testenv.TestEnv, conn *grpc.ClientConn) string {
	arn := uploadAction(ctx, t, env, "", repb.DigestFunction_SHA256, &repb.Action{})
	execCtx, cancel := context.WithCancel(ctx)
	stream, err := repb.NewExecutionClient(conn).Execute(execCtx, &repb.ExecuteRequest{
		InstanceName:   arn.GetInstanceName(),
		ActionDigest:   arn.GetDigest(),
		DigestFunction: arn.GetDigestFunction(),
	})
	require.NoError(t, err)
	op, err := stream.Recv()
	require.NoError(t, err)
	require.False(t, op.GetDone())
	require.Len(t, env.GetSchedulerService().(*schedulerServerMock).scheduleReqs, 1)
	cancel()
	return op.GetName()
}

// Claim A, case 2: the task was dispatched, then the scheduler lost it (or
// deleted it after its final attempt) without publishing a final update.
// The mock scheduler holds no task state, so from the execution server's
// point of view the task is gone and nobody will publish again.
func TestWaitExecutionRepro_DispatchedTaskLostWithoutFinalUpdate(t *testing.T) {
	for _, tc := range monitoringCases() {
		t.Run(tc.name, func(t *testing.T) {
			env, conn, ctx := setupWaitTest(t, tc.monitoring)
			name := startExecutionAndDisconnect(ctx, t, env, conn)
			out := callWaitExecution(ctx, t, conn, name)
			requireWaitEnded(t, out)
		})
	}
}

// Control: once a final update has been published (as MarkExecutionFailed
// does when the scheduler gives up on a task), WaitExecution returns it.
func TestWaitExecutionRepro_ControlFinalUpdatePublished(t *testing.T) {
	for _, tc := range monitoringCases() {
		t.Run(tc.name, func(t *testing.T) {
			env, conn, ctx := setupWaitTest(t, tc.monitoring)
			name := startExecutionAndDisconnect(ctx, t, env, conn)
			pctx, err := prefix.AttachUserPrefixToContext(ctx, env.GetAuthenticator())
			require.NoError(t, err)
			err = env.GetRemoteExecutionService().MarkExecutionFailed(pctx, name, status.InternalError("task already attempted 5 times"))
			require.NoError(t, err)
			out := callWaitExecution(ctx, t, conn, name)
			require.NoError(t, out.err)
			require.NotEmpty(t, out.ops)
			last := out.ops[len(out.ops)-1]
			require.True(t, last.GetDone())
			require.Equal(t, int32(codes.Internal), operation.ExtractExecuteResponse(last).GetStatus().GetCode())
		})
	}
}

// Claim B: Execute served from the action cache returns an operation name
// that WaitExecution does not know about.
func TestWaitExecutionRepro_CachedExecuteOperation(t *testing.T) {
	for _, tc := range monitoringCases() {
		t.Run(tc.name, func(t *testing.T) {
			env, conn, ctx := setupWaitTest(t, tc.monitoring)
			arn := uploadAction(ctx, t, env, "", repb.DigestFunction_SHA256, &repb.Action{})
			acrn := digest.NewACResourceName(arn.GetDigest(), arn.GetInstanceName(), arn.GetDigestFunction())
			err := cachetools.UploadActionResult(ctx, env.GetActionCacheClient(), acrn, &repb.ActionResult{ExitCode: 0})
			require.NoError(t, err)

			stream, err := repb.NewExecutionClient(conn).Execute(ctx, &repb.ExecuteRequest{
				InstanceName:   arn.GetInstanceName(),
				ActionDigest:   arn.GetDigest(),
				DigestFunction: arn.GetDigestFunction(),
			})
			require.NoError(t, err)
			op, err := stream.Recv()
			require.NoError(t, err)
			require.True(t, op.GetDone(), "expected a cache hit")
			require.True(t, operation.ExtractExecuteResponse(op).GetCachedResult())
			require.Empty(t, env.GetSchedulerService().(*schedulerServerMock).scheduleReqs, "cache hit should not schedule")
			t.Logf("cached Execute returned operation %q", op.GetName())

			out := callWaitExecution(ctx, t, conn, op.GetName())
			requireWaitEnded(t, out)
		})
	}
}
