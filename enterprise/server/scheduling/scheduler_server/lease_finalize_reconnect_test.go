package scheduler_server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/scheduling/task_leaser"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testredis"
	"github.com/buildbuddy-io/buildbuddy/server/environment"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/platform"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	scpb "github.com/buildbuddy-io/buildbuddy/proto/scheduler"
)

// routingSchedulerClient emulates the load balancer in front of the app
// replicas: new LeaseTask streams and ReEnqueueTask RPCs go to whichever
// replica is currently the target.
type routingSchedulerClient struct {
	scpb.SchedulerClient // unused methods panic

	mu     sync.Mutex
	target scpb.SchedulerClient
}

func (c *routingSchedulerClient) setTarget(target scpb.SchedulerClient) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.target = target
}

func (c *routingSchedulerClient) get() scpb.SchedulerClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.target
}

func (c *routingSchedulerClient) LeaseTask(ctx context.Context, opts ...grpc.CallOption) (scpb.Scheduler_LeaseTaskClient, error) {
	return c.get().LeaseTask(ctx, opts...)
}

func (c *routingSchedulerClient) ReEnqueueTask(ctx context.Context, req *scpb.ReEnqueueTaskRequest, opts ...grpc.CallOption) (*scpb.ReEnqueueTaskResponse, error) {
	return c.get().ReEnqueueTask(ctx, req, opts...)
}

// executorEnv is the environment of the (real) executor-side TaskLeaser: it
// reaches the apps through the routing client.
type executorEnv struct {
	environment.Env
	schedulerClient scpb.SchedulerClient
}

func (e *executorEnv) GetSchedulerClient() scpb.SchedulerClient {
	return e.schedulerClient
}

// recordingExecutionService records MarkExecutionFailed calls.
type recordingExecutionService struct {
	interfaces.RemoteExecutionService

	mu     sync.Mutex
	failed map[string]error
}

func (r *recordingExecutionService) MarkExecutionFailed(ctx context.Context, taskID string, reason error) error {
	r.mu.Lock()
	r.failed[taskID] = reason
	r.mu.Unlock()
	return r.RemoteExecutionService.MarkExecutionFailed(ctx, taskID, reason)
}

func (r *recordingExecutionService) failure(taskID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	err, ok := r.failed[taskID]
	return ok, err
}

type twoReplicaSetup struct {
	app1, app2       *testenv.TestEnv
	ctx              context.Context
	router           *routingSchedulerClient
	leaser           *task_leaser.TaskLeaser
	executor         *fakeExecutor
	recordingService *recordingExecutionService
}

// setupTwoReplicas starts two scheduler servers (app replicas) that share one
// Redis. A fake executor is registered with app2 (so that app2 can hand out
// reservations), and a real TaskLeaser leases tasks through the router, which
// initially targets app1.
func setupTwoReplicas(t *testing.T, opts *schedulerOpts) *twoReplicaSetup {
	return setupTwoReplicasWithShutdown(t, opts, 1*time.Second)
}

// maxShutdown is app1's max_shutdown_duration: its gRPC server hard-stops
// 100ms before it is up.
func setupTwoReplicasWithShutdown(t *testing.T, opts *schedulerOpts, maxShutdown time.Duration) *twoReplicaSetup {
	testleak.Check(t)
	flags.Set(t, "max_shutdown_duration", maxShutdown)
	redisTarget := testredis.Start(t).Target
	app1, _ := getEnvWithRedis(t, opts, "", redisTarget)
	app2, ctx := getEnvWithRedis(t, opts, "user1", redisTarget)
	rec := &recordingExecutionService{
		RemoteExecutionService: app2.GetRemoteExecutionService(),
		failed:                 map[string]error{},
	}
	app2.SetRemoteExecutionService(rec)

	fe := newFakeExecutor(ctx, t, app2.GetSchedulerClient())
	fe.Register()

	router := &routingSchedulerClient{target: app1.GetSchedulerClient()}
	leaser := task_leaser.NewTaskLeaser(&executorEnv{Env: app2, schedulerClient: router}, "real-executor", "real-executor-host", false)
	return &twoReplicaSetup{
		app1: app1, app2: app2, ctx: ctx, router: router, leaser: leaser,
		executor: fe, recordingService: rec,
	}
}

// shutDownApp1 emulates a rolling deploy of app1: the load balancer stops
// routing to it and it shuts down, hard-stopping any lease streams that are
// still open once max_shutdown_duration is (nearly) up.
func (s *twoReplicaSetup) shutDownApp1() {
	s.router.setTarget(s.app2.GetSchedulerClient())
	s.app1.GetHealthChecker().Shutdown()
	s.app1.GetHealthChecker().WaitForGracefulShutdown()
	// The test health checker does not stop the gRPC server, so do what
	// grpc_server.GRPCShutdown does once the shutdown deadline is (nearly)
	// up: hard-stop it. This breaks lease streams that are still open. Stop
	// waits for the handlers to return (grpc.WaitForHandlers).
	s.app1.GetGRPCServer().Stop()
}

func (s *twoReplicaSetup) taskExists(t *testing.T, taskID string) bool {
	exists, err := s.app2.GetSchedulerService().ExistsTask(s.ctx, taskID)
	require.NoError(t, err)
	return exists
}

// An executor runs a task with retries disabled. Its lease stream is on app1.
// app1 is shut down during a rolling deploy while the task is running; the
// scheduler keeps the task for the executor to reconnect (it is not an
// execution retry). The task then completes successfully: the executor has
// already published the successful ExecuteResponse, and closes the lease with
// no error. The execution must not be marked failed.
func TestLeaseClose_FinalizeAfterAppShutdown_RetriesDisabled_NotMarkedFailed(t *testing.T) {
	s := setupTwoReplicas(t, &schedulerOpts{})
	taskID := scheduleTask(s.ctx, t, s.app2, map[string]string{platform.RetryPropertyName: "false"})
	s.executor.WaitForTask(taskID)

	lease, err := s.leaser.Lease(s.ctx, taskID)
	require.NoError(t, err)

	s.shutDownApp1()
	require.True(t, s.taskExists(t, taskID), "task should be kept for its executor to reconnect")

	// The task completed successfully (result already published).
	lease.Close(s.ctx, nil /*=taskErr*/, false /*=retry*/)

	if failed, reason := s.recordingService.failure(taskID); failed {
		require.FailNowf(t, "successfully completed task was marked failed", "task %q: MarkExecutionFailed(%v)", taskID, reason)
	}
	require.False(t, s.taskExists(t, taskID), "completed task should have been finalized")
}

// Same as above, with retries enabled (the default): a task that completed
// successfully must not be re-enqueued and run again.
func TestLeaseClose_FinalizeAfterAppShutdown_NotReRun(t *testing.T) {
	s := setupTwoReplicas(t, &schedulerOpts{})
	taskID := scheduleTask(s.ctx, t, s.app2, map[string]string{})
	s.executor.WaitForTask(taskID)

	lease, err := s.leaser.Lease(s.ctx, taskID)
	require.NoError(t, err)
	s.executor.ResetTasks()

	s.shutDownApp1()
	require.True(t, s.taskExists(t, taskID))

	// The task completed successfully (result already published).
	lease.Close(s.ctx, nil /*=taskErr*/, false /*=retry*/)

	// Give any re-enqueue time to reach the executor.
	time.Sleep(time.Second)
	s.executor.mu.Lock()
	_, reEnqueued := s.executor.tasks[taskID]
	s.executor.mu.Unlock()
	if reEnqueued {
		thief := newFakeExecutorWithId(s.ctx, t, "thief", s.app2.GetSchedulerClient())
		_, claimErr := thief.leaseTask(taskID, "" /*=reconnectToken*/)
		require.FailNowf(t, "successfully completed task was re-enqueued", "task %q was offered to an executor again after it completed; another executor's claim error: %v", taskID, claimErr)
	}
	require.False(t, s.taskExists(t, taskID), "completed task should have been finalized")
}

// Control: if the executor's next message after app1's shutdown is a lease
// renewal (rather than the final Finalize), the TaskLease reconnects to app2
// and the task then finalizes cleanly. This shows that the harness's
// reconnect path works, and that only Close lacks it.
func TestLeaseClose_RenewalAfterAppShutdown_ReconnectsAndFinalizes(t *testing.T) {
	// Renew the lease every second, and keep app1 in graceful shutdown for
	// long enough that a renewal reaches it before the hard stop.
	s := setupTwoReplicasWithShutdown(t, &schedulerOpts{options: Options{LeaseDuration: 1 * time.Second}}, 4*time.Second)
	taskID := scheduleTask(s.ctx, t, s.app2, map[string]string{platform.RetryPropertyName: "false"})
	s.executor.WaitForTask(taskID)

	lease, err := s.leaser.Lease(s.ctx, taskID)
	require.NoError(t, err)

	// Begin app1's shutdown, but let a renewal reach it (and reconnect to
	// app2) before the hard stop.
	s.router.setTarget(s.app2.GetSchedulerClient())
	s.app1.GetHealthChecker().Shutdown()
	s.app1.GetHealthChecker().WaitForGracefulShutdown()
	time.Sleep(1500 * time.Millisecond)
	s.app1.GetGRPCServer().Stop()
	time.Sleep(1000 * time.Millisecond)
	require.NoError(t, lease.Context().Err(), "lease should have survived the app1 shutdown")

	lease.Close(s.ctx, nil /*=taskErr*/, false /*=retry*/)

	if failed, reason := s.recordingService.failure(taskID); failed {
		require.FailNowf(t, "successfully completed task was marked failed", "task %q: MarkExecutionFailed(%v)", taskID, reason)
	}
	require.False(t, s.taskExists(t, taskID), "completed task should have been finalized")
}

// An executor is running a task whose lease stream is on app1. app1 is shut
// down, and its gRPC server hard-stops the lease stream before the executor's
// next renewal (renewals happen every lease_duration; the hard stop comes
// max_shutdown_duration - shutdown_lameduck_duration after shutdown begins,
// which can be shorter than one lease_duration). The scheduler keeps the
// task for the executor to reconnect, so the executor's next renewal should
// reconnect to app2 and the task should keep running.
func TestLeaseRenewal_AfterAppHardStop_Reconnects(t *testing.T) {
	// Renew the lease every 2s; app1 hard-stops 400ms into its shutdown.
	s := setupTwoReplicasWithShutdown(t, &schedulerOpts{options: Options{LeaseDuration: 2 * time.Second}}, 500*time.Millisecond)
	taskID := scheduleTask(s.ctx, t, s.app2, map[string]string{platform.RetryPropertyName: "false"})
	s.executor.WaitForTask(taskID)

	lease, err := s.leaser.Lease(s.ctx, taskID)
	require.NoError(t, err)

	s.shutDownApp1()
	require.True(t, s.taskExists(t, taskID), "task should be kept for its executor to reconnect")

	// Wait past the first renewal.
	time.Sleep(3 * time.Second)
	leaseErr := lease.Context().Err()

	lease.Close(s.ctx, nil /*=taskErr*/, false /*=retry*/)
	require.NoError(t, leaseErr, "executor gave up its lease (and canceled the running task) instead of reconnecting")
}
