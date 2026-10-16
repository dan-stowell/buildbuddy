package action_merger

import (
	"context"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testredis"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/nullauth"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
)

type pendingScheduler struct{ interfaces.SchedulerService }

func (*pendingScheduler) ExistsTask(context.Context, string) (bool, error) { return true, nil }

func TestHedgeLimitSequential(t *testing.T)          { testHedgeLimit(t, false) }
func TestHedgeLimitOverlappingRequests(t *testing.T) { testHedgeLimit(t, true) }

func testHedgeLimit(t *testing.T, overlapping bool) {
	flags.Set(t, "remote_execution.enable_action_merging", true)
	flags.Set(t, "remote_execution.action_merging_hedge_count", 1)
	flags.Set(t, "remote_execution.action_merging_hedge_delay", time.Microsecond)
	ctx, err := prefix.AttachUserPrefixToContext(t.Context(), nullauth.NewNullAuthenticator(true))
	require.NoError(t, err)
	rdb := testredis.Start(t).Client()
	action := digest.NewCASResourceName(&repb.Digest{Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SizeBytes: 1}, "", repb.DigestFunction_SHA256)
	scheduler := &pendingScheduler{}
	canonical, op := GetOrCreateExecutionID(ctx, rdb, scheduler, action, false)
	require.Equal(t, New, op)
	// Make the hedge eligible without sleeps or depending on scheduling speed.
	key, err := redisKeyForPendingExecutionID(ctx, action)
	require.NoError(t, err)
	require.NoError(t, rdb.HSet(ctx, key, lastExecutionSubmitTimeKey, "1").Err())
	id1, decision1 := GetOrCreateExecutionID(ctx, rdb, scheduler, action, false)
	require.Equal(t, canonical, id1)
	require.Equal(t, Hedge, decision1)
	if !overlapping {
		require.NoError(t, RecordHedgedExecution(ctx, rdb, action, ""))
	}
	// A valid concurrent interleaving: both requests decide before either
	// records its hedge, exactly as Execute's separate calls allow.
	id2, decision2 := GetOrCreateExecutionID(ctx, rdb, scheduler, action, false)
	require.Equal(t, canonical, id2)
	if overlapping {
		require.NoError(t, RecordHedgedExecution(ctx, rdb, action, ""))
	}
	dispatched := 1
	if decision2 == Hedge {
		require.NoError(t, RecordHedgedExecution(ctx, rdb, action, ""))
		dispatched++
	}
	t.Logf("hedge_count=1; decisions=(%v,%v); dispatched=%d; overlapping=%v", decision1, decision2, dispatched, overlapping)
	require.LessOrEqual(t, dispatched, 1, "overlapping requests must share the hedge budget")
}

func TestCopyCompletionOrdering(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		firstSuccess, lastSuccess bool
		firstHedge                bool
	}{
		{"hedge wins", true, true, true},
		{"original wins", true, true, false},
		{"failed original then hedge succeeds", false, true, false},
		{"failed hedge then original succeeds", false, true, true},
		{"all fail original last", false, false, true},
		{"all fail hedge last", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			rdb := testredis.Start(t).Client()
			action := digest.NewCASResourceName(&repb.Digest{Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SizeBytes: 1}, "", repb.DigestFunction_SHA256)
			original, hedge := action.NewUploadString(), action.NewUploadString()
			require.NoError(t, RegisterCopy(ctx, rdb, original, original))
			require.NoError(t, RegisterCopy(ctx, rdb, original, hedge))
			first, last := original, hedge
			if tc.firstHedge {
				first, last = hedge, original
			}
			canonical, publish, copies, err := CompleteCopy(ctx, rdb, first, tc.firstSuccess)
			require.NoError(t, err)
			require.Equal(t, original, canonical)
			require.Equal(t, tc.firstSuccess, publish)
			if publish {
				require.ElementsMatch(t, []string{original, hedge}, copies)
			}
			canonical, publish, _, err = CompleteCopy(ctx, rdb, last, tc.lastSuccess)
			require.NoError(t, err)
			require.Equal(t, original, canonical)
			require.Equal(t, !tc.firstSuccess, publish)
			winner, success := last, tc.lastSuccess
			if tc.firstSuccess {
				winner, success = first, true
			}
			// A retry must repeat cleanup as well as publication.
			_, publish, copies, err = CompleteCopy(ctx, rdb, winner, success)
			require.NoError(t, err)
			require.True(t, publish)
			require.ElementsMatch(t, []string{original, hedge}, copies)
			require.Error(t, RegisterCopy(ctx, rdb, original, action.NewUploadString()))
			if success {
				_, publish, _, err = CompleteCopy(ctx, rdb, winner, false)
				require.NoError(t, err)
				require.False(t, publish, "late failure must not replace successful completion")
			}
		})
	}
}

func TestInvocationInterest(t *testing.T) {
	ctx := t.Context()
	rdb := testredis.Start(t).Client()
	action := digest.NewCASResourceName(&repb.Digest{Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SizeBytes: 1}, "", repb.DigestFunction_SHA256)
	original, hedge := action.NewUploadString(), action.NewUploadString()
	require.NoError(t, RegisterCopy(ctx, rdb, original, original))
	require.NoError(t, RegisterCopy(ctx, rdb, original, hedge))
	for _, invocation := range []string{"a", "a", "b"} {
		require.NoError(t, AddInvocation(ctx, rdb, original, invocation))
	}
	_, copies, err := RemoveInvocation(ctx, rdb, original, "a")
	require.NoError(t, err)
	require.Empty(t, copies)
	_, copies, err = RemoveInvocation(ctx, rdb, hedge, "b")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{original, hedge}, copies)
	_, publish, _, err := CompleteCopy(ctx, rdb, hedge, true)
	require.NoError(t, err)
	require.False(t, publish)
	require.Error(t, RegisterCopy(ctx, rdb, original, action.NewUploadString()))
}
