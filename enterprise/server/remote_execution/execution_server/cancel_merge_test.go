package execution_server_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/operation"
	"github.com/buildbuddy-io/buildbuddy/proto/invocation_status"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/tables"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/util/bazel_request"
	"github.com/buildbuddy-io/buildbuddy/server/util/perms"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	scpb "github.com/buildbuddy-io/buildbuddy/proto/scheduler"
)

// trackingScheduler is a scheduler mock that, like the real redis-backed
// scheduler, remembers which tasks exist so that ExistsTask (used by action
// merging) and CancelTask behave realistically.
type trackingScheduler struct {
	*schedulerServerMock

	mu        sync.Mutex
	tasks     map[string]bool
	scheduled []string
	// onCancel, if set, is invoked at the start of CancelTask (before the task
	// is deleted). Used to deterministically interleave another client.
	onCancel func(taskID string)
}

func newTrackingScheduler() *trackingScheduler {
	return &trackingScheduler{
		schedulerServerMock: &schedulerServerMock{taskLost: make(chan struct{})},
		tasks:               map[string]bool{},
	}
}

func (s *trackingScheduler) ScheduleTask(ctx context.Context, req *scpb.ScheduleTaskRequest) (*scpb.ScheduleTaskResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[req.GetTaskId()] = true
	s.scheduled = append(s.scheduled, req.GetTaskId())
	return &scpb.ScheduleTaskResponse{}, nil
}

func (s *trackingScheduler) CancelTask(ctx context.Context, taskID string) (bool, error) {
	if s.onCancel != nil {
		s.onCancel(taskID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existed := s.tasks[taskID]
	delete(s.tasks, taskID)
	return existed, nil
}

func (s *trackingScheduler) ExistsTask(ctx context.Context, taskID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks[taskID], nil
}

func (s *trackingScheduler) numScheduled() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.scheduled)
}

type cancelMergeHarness struct {
	t      *testing.T
	ctx    context.Context // authenticated as US1
	client repb.ExecutionClient
	sched  *trackingScheduler
	cancel func(iid string) error
	arn    *repb.Digest
	env    interface {
		GetDBHandle() interfaces.DBHandle
	}
}

func setupCancelMerge(t *testing.T) *cancelMergeHarness {
	env, conn, _ := setupEnv(t)
	sched := newTrackingScheduler()
	env.SetSchedulerService(sched)
	ta := testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1"))
	env.SetAuthenticator(ta)
	ctx, err := ta.WithAuthenticatedUser(context.Background(), "US1")
	require.NoError(t, err)
	arn := uploadAction(ctx, t, env, "" /*=instanceName*/, repb.DigestFunction_SHA256, &repb.Action{})
	h := &cancelMergeHarness{
		t:      t,
		ctx:    ctx,
		client: repb.NewExecutionClient(conn),
		sched:  sched,
		arn:    arn.GetDigest(),
		env:    env,
	}
	h.cancel = func(iid string) error {
		return env.GetRemoteExecutionService().Cancel(ctx, iid)
	}
	return h
}

func (h *cancelMergeHarness) newInvocation() string {
	iid := uuid.NewString()
	createInvocation(h.ctx, h.t, h.env.GetDBHandle(), &tables.Invocation{
		InvocationID:     iid,
		InvocationStatus: int64(invocation_status.InvocationStatus_PARTIAL_INVOCATION_STATUS),
		Perms:            perms.GROUP_READ | perms.GROUP_WRITE,
		GroupID:          "GR1",
	})
	return iid
}

func (h *cancelMergeHarness) execute(iid string) repb.Execution_ExecuteClient {
	clientCtx, err := bazel_request.WithRequestMetadata(h.ctx, &repb.RequestMetadata{ToolInvocationId: iid})
	require.NoError(h.t, err)
	stream, err := h.client.Execute(clientCtx, &repb.ExecuteRequest{
		ActionDigest:   h.arn,
		DigestFunction: repb.DigestFunction_SHA256,
	})
	require.NoError(h.t, err)
	return stream
}

// recvWithTimeout returns the next operation from the stream, or ok=false if
// none arrives within the timeout.
func recvWithTimeout(t *testing.T, stream repb.Execution_ExecuteClient, timeout time.Duration) (*repb.ExecuteResponse, bool, bool) {
	type res struct {
		done bool
		rsp  *repb.ExecuteResponse
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		op, err := stream.Recv()
		if err != nil {
			ch <- res{err: err}
			return
		}
		ch <- res{done: op.GetDone(), rsp: operation.ExtractExecuteResponse(op)}
	}()
	select {
	case r := <-ch:
		require.NoError(t, r.err)
		return r.rsp, r.done, true
	case <-time.After(timeout):
		return nil, false, false
	}
}

// Invocation A runs an action; the user cancels invocation A (e.g. from the
// UI, or a workflow is cancelled) while the action is still queued. The user
// then re-runs the build (invocation B), which submits the identical action.
// B's execution should be dispatched promptly; the cancelled execution must not
// be considered a merge candidate.
func TestCancel_ThenRerunIdenticalActionIsDispatchedPromptly(t *testing.T) {
	h := setupCancelMerge(t)

	iidA := h.newInvocation()
	streamA := h.execute(iidA)
	_, done, ok := recvWithTimeout(t, streamA, 10*time.Second)
	require.True(t, ok)
	require.False(t, done)
	require.Equal(t, 1, h.sched.numScheduled())

	require.NoError(t, h.cancel(iidA))
	rspA, done, ok := recvWithTimeout(t, streamA, 10*time.Second)
	require.True(t, ok)
	require.True(t, done)
	require.Equal(t, int32(codes.Canceled), rspA.GetStatus().GetCode())

	// Re-run: identical action from a new invocation.
	iidB := h.newInvocation()
	start := time.Now()
	streamB := h.execute(iidB)
	_, done, ok = recvWithTimeout(t, streamB, 30*time.Second)
	elapsed := time.Since(start)
	require.True(t, ok)
	require.False(t, done)
	require.Equal(t, 2, h.sched.numScheduled(), "rerun should have dispatched a new execution")
	require.Less(t, elapsed, 2*time.Second, "rerun of an action whose previous execution was cancelled was delayed by %s", elapsed)
}

// Invocation A's execution is not merged at the time Cancel(A) checks
// CheckMerged, but invocation B merges onto it before CancelTask runs. B must
// not receive A's cancellation.
func TestCancel_RacingMergedClientIsNotCancelled(t *testing.T) {
	h := setupCancelMerge(t)

	iidA := h.newInvocation()
	streamA := h.execute(iidA)
	_, done, ok := recvWithTimeout(t, streamA, 10*time.Second)
	require.True(t, ok)
	require.False(t, done)

	iidB := h.newInvocation()
	var streamB repb.Execution_ExecuteClient
	h.sched.onCancel = func(taskID string) {
		h.sched.onCancel = nil
		// B's request arrives between CheckMerged and CancelTask.
		streamB = h.execute(iidB)
		_, done, ok := recvWithTimeout(t, streamB, 10*time.Second)
		require.True(t, ok)
		require.False(t, done)
	}
	require.NoError(t, h.cancel(iidA))
	require.NotNil(t, streamB)
	require.Equal(t, 1, h.sched.numScheduled(), "B should have merged onto A's execution")

	rspB, done, ok := recvWithTimeout(t, streamB, 3*time.Second)
	if ok && done {
		require.NotEqual(t, int32(codes.Canceled), rspB.GetStatus().GetCode(),
			"invocation B (merged) received invocation A's cancellation: %v", rspB.GetStatus())
	}
}
