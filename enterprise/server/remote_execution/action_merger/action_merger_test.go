package action_merger

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"

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

// Baseline: a merge recorded after the execution completed normally leaves a
// persistent action-count, so the next lone execution looks merged.
func TestPreexisting_LateMergeAfterCompletion(t *testing.T) {
	h := newHarness(t, testredis.Start(t).Client())
	ad := randomAction("")
	p := h.newScheduled(ad)
	h.mustMerge(ad, p)
	require.NoError(t, DeletePendingExecution(h.ctx, h.rdb, p))
	require.NoError(t, RecordMergedExecution(h.ctx, h.rdb, ad, "GR1"))
	fwd, err := redisKeyForPendingExecutionID(h.ctx, ad)
	require.NoError(t, err)
	ttl := h.rdb.TTL(h.ctx, fwd).Val()
	t.Logf("forward key after late merge: %v ttl=%v", h.rdb.HGetAll(h.ctx, fwd).Val(), ttl)
	n := h.newScheduled(ad)
	merged, err := CheckMerged(h.ctx, h.rdb, ad)
	require.NoError(t, err)
	require.False(t, merged, "lone execution %q looks merged (forward key ttl after late merge: %v)", n, ttl)
}

// Baseline: on a ring, merging does not work when the instance name contains
// a hash tag.
func TestPreexisting_InstanceNameWithBracesOnRing(t *testing.T) {
	h := newHarness(t, testredis.StartSharded(t, 4).Client())
	notMerged := 0
	for i := range 10 {
		ad := randomAction(fmt.Sprintf("inst{%d}", i))
		p := h.newScheduled(ad)
		if id, op := GetOrCreateExecutionID(h.ctx, h.rdb, h.sched, ad, false); op != Merge || id != p {
			notMerged++
		}
	}
	require.Zero(t, notMerged, "identical requests not merged in %d/10 cases", notMerged)
}
