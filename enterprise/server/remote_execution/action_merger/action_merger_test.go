package action_merger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testredis"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/nullauth"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

type pendingScheduler struct{ interfaces.SchedulerService }

func (*pendingScheduler) ExistsTask(context.Context, string) (bool, error) { return true, nil }

type afterMappingRead struct {
	key string
	run func()
}

func (*afterMappingRead) BeforeProcess(ctx context.Context, _ redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (h *afterMappingRead) AfterProcess(_ context.Context, cmd redis.Cmder) error {
	if cmd.Name() == "get" && cmd.Args()[1] == h.key && h.run != nil {
		run := h.run
		h.run = nil
		run()
	}
	return nil
}
func (*afterMappingRead) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (*afterMappingRead) AfterProcessPipeline(context.Context, []redis.Cmder) error { return nil }

func TestHedgeRegistrationCompletionOrdering(t *testing.T) {
	for _, order := range []string{"completion first", "registration first", "registration during completion lookup"} {
		t.Run(order, func(t *testing.T) {
			flags.Set(t, "remote_execution.enable_action_merging", true)
			flags.Set(t, "remote_execution.action_merging_hedge_count", 1)
			ctx, err := prefix.AttachUserPrefixToContext(t.Context(), nullauth.NewNullAuthenticator(true))
			require.NoError(t, err)
			rdb := testredis.Start(t).Client()
			action := digest.NewCASResourceName(&repb.Digest{Hash: strings.Repeat("a", 64), SizeBytes: 1}, "", repb.DigestFunction_SHA256)
			canonical, decision := GetOrCreateExecutionID(ctx, rdb, &pendingScheduler{}, action, false)
			require.Equal(t, New, decision)
			hedge := action.NewUploadString()
			register := func() { require.NoError(t, RegisterHedge(ctx, rdb, canonical, hedge)) }
			switch order {
			case "registration first":
				register()
			case "registration during completion lookup":
				rdb.AddHook(&afterMappingRead{key: redisKeyForPendingExecutionDigest(canonical), run: register})
			}
			id, publish, copies, err := CompleteCopy(ctx, rdb, canonical, true, []byte("terminal"))
			require.NoError(t, err)
			require.Equal(t, canonical, id)
			require.True(t, publish)
			if order == "completion first" {
				require.Empty(t, copies)
				require.Error(t, RegisterHedge(ctx, rdb, canonical, hedge))
				require.Zero(t, rdb.Exists(ctx, copyKey(canonical), copyKey(hedge)).Val())
				return
			}
			require.ElementsMatch(t, []string{canonical, hedge}, copies)
			require.NoError(t, DeletePendingExecution(ctx, rdb, canonical))
			// Late lease refresh must neither restore the merge mapping nor lose fencing.
			require.NoError(t, RecordClaimedExecution(ctx, rdb, hedge, time.Minute))
			id, publish, _, err = CompleteCopy(ctx, rdb, hedge, true, []byte("late"))
			require.NoError(t, err)
			require.Equal(t, canonical, id)
			require.False(t, publish)
			data, err := CompletedOperation(ctx, rdb, canonical)
			require.NoError(t, err)
			require.Equal(t, "terminal", string(data))
		})
	}
}

func TestHedgePairRegistrationReservesBudget(t *testing.T) {
	flags.Set(t, "remote_execution.enable_action_merging", true)
	flags.Set(t, "remote_execution.action_merging_hedge_count", 1)
	ctx, err := prefix.AttachUserPrefixToContext(t.Context(), nullauth.NewNullAuthenticator(true))
	require.NoError(t, err)
	rdb := testredis.Start(t).Client()
	action := digest.NewCASResourceName(&repb.Digest{Hash: strings.Repeat("b", 64), SizeBytes: 1}, "", repb.DigestFunction_SHA256)
	canonical, _ := GetOrCreateExecutionID(ctx, rdb, &pendingScheduler{}, action, false)
	first, second := action.NewUploadString(), action.NewUploadString()
	require.NoError(t, RegisterHedge(ctx, rdb, canonical, first))
	require.Error(t, RegisterHedge(ctx, rdb, canonical, second))
	require.Zero(t, rdb.Exists(ctx, copyKey(second)).Val())
	require.NoError(t, RecordClaimedExecution(ctx, rdb, first, time.Minute))
	forward, err := redisKeyForPendingExecutionID(ctx, action)
	require.NoError(t, err)
	require.Equal(t, canonical, rdb.HGet(ctx, forward, executionIDKey).Val())
}
