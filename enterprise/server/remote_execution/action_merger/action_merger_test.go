package action_merger

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testredis"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// fakeScheduler only implements ExistsTask, which is all action merging needs.
type fakeScheduler struct {
	interfaces.SchedulerService
	mu    sync.Mutex
	tasks map[string]bool
}

func (s *fakeScheduler) ExistsTask(ctx context.Context, taskID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks[taskID], nil
}

func (s *fakeScheduler) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[id] = true
}

type harness struct {
	t     *testing.T
	ctx   context.Context
	rdb   redis.UniversalClient
	sched *fakeScheduler
}

func newHarness(t *testing.T, rdb redis.UniversalClient) *harness {
	auth := testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1"))
	ctx, err := auth.WithAuthenticatedUser(context.Background(), "US1")
	require.NoError(t, err)
	ctx, err = prefix.AttachUserPrefixToContext(ctx, auth)
	require.NoError(t, err)
	return &harness{t: t, ctx: ctx, rdb: rdb, sched: &fakeScheduler{tasks: map[string]bool{}}}
}

func randomAction(instanceName string) *digest.CASResourceName {
	h := sha256.Sum256([]byte(uuid.NewString()))
	return digest.NewCASResourceName(&repb.Digest{Hash: fmt.Sprintf("%x", h), SizeBytes: 123}, instanceName, repb.DigestFunction_SHA256)
}

// newScheduled registers a new execution for the action and marks it as
// scheduled.
func (h *harness) newScheduled(ad *digest.CASResourceName) string {
	id, op := GetOrCreateExecutionID(h.ctx, h.rdb, h.sched, ad, false)
	require.Equal(h.t, New, op)
	h.sched.add(id)
	return id
}

func (h *harness) mustMerge(ad *digest.CASResourceName, want string) {
	id, op := GetOrCreateExecutionID(h.ctx, h.rdb, h.sched, ad, false)
	require.Equal(h.t, Merge, op)
	require.Equal(h.t, want, id)
}

func clients(t *testing.T) map[string]func() redis.UniversalClient {
	return map[string]func() redis.UniversalClient{
		"single": func() redis.UniversalClient { return testredis.Start(t).Client() },
		"ring":   func() redis.UniversalClient { return testredis.StartSharded(t, 4).Client() },
	}
}

// Cancel decides first: a request that already looked up the execution must
// not be able to merge into it afterwards.
func TestStopThenRecord_MergeIsRefused(t *testing.T) {
	for name, mk := range clients(t) {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, mk())
			for range 10 {
				ad := randomAction("")
				p := h.newScheduled(ad)
				h.mustMerge(ad, p) // looked up, not yet recorded
				merged, err := StopMergingUnlessMerged(h.ctx, h.rdb, ad, p)
				require.NoError(t, err)
				require.False(t, merged)
				ok, err := RecordMergedExecution(h.ctx, h.rdb, ad, p, "GR1")
				require.NoError(t, err)
				require.False(t, ok, "merge into a cancelled execution must be refused")
				// A fresh lookup must not return the cancelled execution.
				id, op := GetOrCreateExecutionID(h.ctx, h.rdb, h.sched, ad, false)
				require.Equal(t, New, op)
				require.NotEqual(t, p, id)
				// The cancelled-execution marker must not affect the new execution.
				h.sched.add(id)
				h.mustMerge(ad, id)
				ok, err = RecordMergedExecution(h.ctx, h.rdb, ad, id, "GR1")
				require.NoError(t, err)
				require.True(t, ok)
			}
		})
	}
}

// Merge decides first: Cancel must see it and skip the execution.
func TestRecordThenStop_CancelIsSkipped(t *testing.T) {
	for name, mk := range clients(t) {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, mk())
			for range 10 {
				ad := randomAction("")
				p := h.newScheduled(ad)
				h.mustMerge(ad, p)
				ok, err := RecordMergedExecution(h.ctx, h.rdb, ad, p, "GR1")
				require.NoError(t, err)
				require.True(t, ok)
				merged, err := StopMergingUnlessMerged(h.ctx, h.rdb, ad, p)
				require.NoError(t, err)
				require.True(t, merged)
				// Merging is still possible afterwards.
				h.mustMerge(ad, p)
			}
		})
	}
}

// On a sharded (ring) client, the marker must live on the same shard as the
// forward key, otherwise the two scripts would not see each other's writes.
func TestMarkerOnSameShardAsForwardKey(t *testing.T) {
	ring := testredis.StartSharded(t, 4)
	h := newHarness(t, ring.Client())
	for range 20 {
		ad := randomAction("")
		p := h.newScheduled(ad)
		fwd, err := redisKeyForPendingExecutionID(h.ctx, ad)
		require.NoError(t, err)
		fwdShard, revShard := -1, -1
		for j, s := range ring.Shards {
			if s.Client().Exists(h.ctx, fwd).Val() == 1 {
				fwdShard = j
			}
			if s.Client().Exists(h.ctx, redisKeyForPendingExecutionDigest(p)).Val() == 1 {
				revShard = j
			}
		}
		require.NotEqual(t, -1, fwdShard)
		require.Equal(t, fwdShard, revShard)
		_, err = StopMergingUnlessMerged(h.ctx, h.rdb, ad, p)
		require.NoError(t, err)
		markerShard := -1
		for j, s := range ring.Shards {
			if s.Client().Exists(h.ctx, redisKeyForCancelledExecution(p)).Val() == 1 {
				markerShard = j
			}
			require.Zero(t, s.Client().Exists(h.ctx, fwd, redisKeyForPendingExecutionDigest(p)).Val(), "keys left on shard %d", j)
		}
		require.Equal(t, fwdShard, markerShard)
	}
}

// An instance name containing a hash tag puts the reverse key and the marker
// on a different slot than the forward key. Correctness should still hold.
func TestInstanceNameWithBraces(t *testing.T) {
	ring := testredis.StartSharded(t, 4)
	h := newHarness(t, ring.Client())
	for i := range 10 {
		ad := randomAction(fmt.Sprintf("inst{%d}", i))
		p := h.newScheduled(ad)
		h.mustMerge(ad, p)
		merged, err := StopMergingUnlessMerged(h.ctx, h.rdb, ad, p)
		require.NoError(t, err)
		require.False(t, merged)
		ok, err := RecordMergedExecution(h.ctx, h.rdb, ad, p, "GR1")
		require.NoError(t, err)
		require.False(t, ok)
		leftover := h.rdb.Exists(h.ctx, redisKeyForPendingExecutionDigest(p)).Val()
		t.Logf("instance %q: reverse key left after stop: %d", ad.GetInstanceName(), leftover)
	}
}

// A normally completed execution has no marker: a late merge is still
// recorded. Check what it leaves behind in redis.
func TestRecordAfterNormalCompletion(t *testing.T) {
	h := newHarness(t, testredis.Start(t).Client())
	ad := randomAction("")
	p := h.newScheduled(ad)
	h.mustMerge(ad, p)
	// Execution completes; PublishOperation / deleteClaimedTask cleans up.
	require.NoError(t, DeletePendingExecution(h.ctx, h.rdb, p))
	ok, err := RecordMergedExecution(h.ctx, h.rdb, ad, p, "GR1")
	require.NoError(t, err)
	require.True(t, ok, "marker must not affect executions that completed normally")

	fwd, err := redisKeyForPendingExecutionID(h.ctx, ad)
	require.NoError(t, err)
	ttl := h.rdb.TTL(h.ctx, fwd).Val()
	t.Logf("forward key after late merge: %v ttl=%v", h.rdb.HGetAll(h.ctx, fwd).Val(), ttl)

	// The next, unrelated execution of the same action.
	n := h.newScheduled(ad)
	merged, err := StopMergingUnlessMerged(h.ctx, h.rdb, ad, n)
	require.NoError(t, err)
	t.Logf("new execution %q considered merged by Cancel: %v", n, merged)
	require.False(t, merged, "a lone execution must not look merged because of a stale late merge (forward key ttl after late merge: %v)", ttl)
}

// Sanity: hedged executions use the same merge path.
func TestStopWithHedge(t *testing.T) {
	flagsSetHedge(t)
	h := newHarness(t, testredis.Start(t).Client())
	ad := randomAction("")
	p := h.newScheduled(ad)
	id, op := GetOrCreateExecutionID(h.ctx, h.rdb, h.sched, ad, false)
	require.Equal(t, Hedge, op)
	require.Equal(t, p, id)
	merged, err := StopMergingUnlessMerged(h.ctx, h.rdb, ad, p)
	require.NoError(t, err)
	require.False(t, merged)
	ok, err := RecordMergedExecution(h.ctx, h.rdb, ad, p, "GR1")
	require.NoError(t, err)
	require.False(t, ok)
}

func flagsSetHedge(t *testing.T) {
	oldCount, oldDelay := *hedgedActionCount, *hedgeAfterDelay
	*hedgedActionCount = 1
	*hedgeAfterDelay = 0 * time.Second
	t.Cleanup(func() { *hedgedActionCount, *hedgeAfterDelay = oldCount, oldDelay })
}
