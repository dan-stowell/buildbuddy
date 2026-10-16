package action_merger

import (
	"context"
	"fmt"
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

type leaseRaceScheduler struct{ interfaces.SchedulerService }

func (*leaseRaceScheduler) ExistsTask(context.Context, string) (bool, error) { return true, nil }

type leaseRaceClient struct {
	redis.UniversalClient
	afterGet func()
	rounds   int
}

func (r *leaseRaceClient) Get(ctx context.Context, key string) *redis.StringCmd {
	r.rounds++
	cmd := r.UniversalClient.Get(ctx, key)
	if r.afterGet != nil {
		f := r.afterGet
		r.afterGet = nil
		f()
	}
	return cmd
}
func (r *leaseRaceClient) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	r.rounds++
	return r.UniversalClient.Eval(ctx, script, keys, args...)
}
func (r *leaseRaceClient) TxPipeline() redis.Pipeliner {
	r.rounds++
	return r.UniversalClient.TxPipeline()
}

func TestLeaseRefreshDoesNotResurrectCompletedMerge(t *testing.T) {
	flags.Set(t, "remote_execution.enable_action_merging", true)
	flags.Set(t, "remote_execution.action_merging_hedge_count", 1)
	ctx, err := prefix.AttachUserPrefixToContext(t.Context(), nullauth.NewNullAuthenticator(true))
	require.NoError(t, err)
	rdb := testredis.Start(t).Client()
	action := digest.NewCASResourceName(&repb.Digest{Hash: strings.Repeat("d", 64), SizeBytes: 1}, "", repb.DigestFunction_SHA256)
	canonical, decision := GetOrCreateExecutionID(ctx, rdb, &leaseRaceScheduler{}, action, false)
	require.Equal(t, New, decision)
	forward, err := redisKeyForPendingExecutionID(ctx, action)
	require.NoError(t, err)
	client := &leaseRaceClient{UniversalClient: rdb}
	// Completion occurs after lease refresh reads the reverse mapping, before
	// the refresh updates the forward mapping. No hedge is dispatched.
	client.afterGet = func() { require.NoError(t, DeletePendingExecution(ctx, rdb, canonical)) }
	require.NoError(t, RecordClaimedExecution(ctx, client, canonical, time.Minute))
	t.Logf("no-hedge lease refresh Redis round trips: %d", client.rounds)
	require.Equal(t, 2, client.rounds)
	require.Zero(t, rdb.Exists(ctx, forward).Val(), "a late lease refresh must not restore a completed execution as mergeable")
}

// Independently compare the same ordinary lease with hedging off and enabled.
// This uses public production entry points and also runs on bughunt-base.
func TestReviewNoHedgeLeaseRoundTripBudget(t *testing.T) {
	flags.Set(t, "remote_execution.enable_action_merging", true)
	flags.Set(t, "remote_execution.action_merging_hedge_delay", 15*time.Minute)
	ctx, err := prefix.AttachUserPrefixToContext(t.Context(), nullauth.NewNullAuthenticator(true))
	require.NoError(t, err)
	rdb := testredis.Start(t).Client()
	action := digest.NewCASResourceName(&repb.Digest{Hash: strings.Repeat("e", 64), SizeBytes: 1}, "", repb.DigestFunction_SHA256)
	canonical, decision := GetOrCreateExecutionID(ctx, rdb, &leaseRaceScheduler{}, action, false)
	require.Equal(t, New, decision)
	for _, count := range []int{0, 1} {
		t.Run(fmt.Sprintf("hedge_count=%d", count), func(t *testing.T) {
			flags.Set(t, "remote_execution.action_merging_hedge_count", count)
			client := &leaseRaceClient{UniversalClient: rdb}
			require.NoError(t, RecordClaimedExecution(ctx, client, canonical, time.Minute))
			t.Logf("hedge_count=%d: ordinary lease used %d Redis round trips; baseline budget=2", count, client.rounds)
			require.Equal(t, 2, client.rounds, "enabling hedging must preserve the ordinary lease budget")
		})
	}
}
