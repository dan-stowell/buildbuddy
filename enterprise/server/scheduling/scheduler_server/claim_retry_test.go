package scheduler_server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/redisfault"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testredis"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/redisutil"
	"github.com/buildbuddy-io/buildbuddy/server/environment"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testhealthcheck"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
)

// getEnvWithFaultyRedis returns a scheduler env whose remote execution Redis
// client is built the same way as the server builds it from
// --remote_execution.redis_target (redisutil.TargetToOpts +
// NewClientWithOpts), with a dialer that can lose replies.
func getEnvWithFaultyRedis(t *testing.T) (*testenv.TestEnv, context.Context, *redisfault.Injector) {
	target := testredis.Start(t).Target
	opts := redisutil.TargetToOpts(target)
	// The scheduler relies on the client library's default retry settings.
	require.Zero(t, opts.MaxRetries, "test assumes the client's default retry settings")
	fi := redisfault.New()
	opts.Dialer = fi.WrapDialer(opts.Dialer)
	rdb, err := redisutil.NewClientWithOpts(opts, testhealthcheck.NewTestingHealthChecker(), "remote_execution_redis")
	require.NoError(t, err)
	t.Cleanup(func() { rdb.Close() })
	env, ctx := getEnv(t, &schedulerOpts{redisClient: rdb}, "user1")
	// Load the claim script so that a claim is a single EVALSHA.
	require.NoError(t, redisAcquireClaim.Load(ctx, rdb).Err())
	return env, ctx, fi
}

func taskState(env environment.Env, taskID string) string {
	s := env.GetSchedulerService().(*SchedulerServer)
	rdb := env.GetRemoteExecutionRedisClient()
	ctx := context.Background()
	key := s.redisKeyForTask(taskID)
	f, err := rdb.HMGet(ctx, key, "claimed", "leaseId", "attemptCount").Result()
	if err != nil {
		return fmt.Sprintf("<error: %s>", err)
	}
	ttl, _ := rdb.PTTL(ctx, key).Result()
	return fmt.Sprintf("claimed=%v leaseId=%v attemptCount=%v ttl=%s", f[0], f[1], f[2], ttl)
}

// otherExecutorCanClaim reports whether a different executor can claim the
// task within the timeout.
func otherExecutorCanClaim(ctx context.Context, t *testing.T, env environment.Env, taskID string, timeout time.Duration) (bool, error) {
	other := newFakeExecutor(ctx, t, env.GetSchedulerClient())
	other.Register()
	var lastErr error
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		lease, err := other.leaseTask(taskID, "" /*=reconnectToken*/)
		if err == nil {
			require.NoError(t, lease.Finalize())
			return true, nil
		}
		lastErr = err
	}
	return false, lastErr
}

// Redis runs an executor's claim, but the reply is lost (connection reset or
// read timeout). The Redis client retries the claim, and the executor must
// still get the lease, since nobody else can hold it.
func TestClaim_LostReplyIsRetriedAndSucceeds(t *testing.T) {
	for _, mode := range []redisfault.Mode{redisfault.EOF, redisfault.Reset, redisfault.Timeout} {
		t.Run(mode.String(), func(t *testing.T) {
			env, ctx, fi := getEnvWithFaultyRedis(t)
			fe := newFakeExecutor(ctx, t, env.GetSchedulerClient())
			fe.Register()
			taskID := scheduleTask(ctx, t, env, map[string]string{})
			fe.WaitForTask(taskID)

			fi.Arm(1, mode, []byte(redisAcquireClaim.Hash()))
			lease, err := fe.leaseTask(taskID, "" /*=reconnectToken*/)
			require.Equal(t, 1, fi.Lost(), "expected exactly one claim reply to be lost")
			require.Equal(t, 2, fi.Sent(), "expected the Redis client to retry the claim once")
			if err != nil {
				ok, otherErr := otherExecutorCanClaim(ctx, t, env, taskID, 3*time.Second)
				require.FailNowf(t, "claim failed after a lost reply",
					"executor's claim returned %v after the client retried; task state: %s; another executor can claim within 3s: %t (last error: %v)",
					err, taskState(env, taskID), ok, otherErr)
			}
			require.NoError(t, lease.Finalize())
			_, err = env.GetSchedulerService().(*SchedulerServer).readTask(ctx, taskID)
			require.True(t, status.IsNotFoundError(err), "task should be deleted after finalize, got %v", err)
		})
	}
}

// Same as above, for an executor reconnecting to its task after the
// scheduler replica holding its lease shut down.
func TestClaim_LostReconnectReplyIsRetriedAndSucceeds(t *testing.T) {
	flags.Set(t, "remote_execution.lease_reconnect_grace_period", 30*time.Second)
	env, ctx, fi := getEnvWithFaultyRedis(t)
	s := env.GetSchedulerService().(*SchedulerServer)
	fe := newFakeExecutor(ctx, t, env.GetSchedulerClient())
	fe.Register()
	taskID := scheduleTask(ctx, t, env, map[string]string{})
	fe.WaitForTask(taskID)
	lease := fe.Claim(taskID)
	require.NoError(t, s.reEnqueueTask(ctx, taskID, lease.leaseID, lease.leaseID /*=reconnectToken*/, 1, "server shutting down"))

	fi.Arm(1, redisfault.EOF, []byte(redisAcquireClaim.Hash()))
	reconnected, err := fe.Reconnect(taskID, lease.leaseID)
	require.Equal(t, 1, fi.Lost())
	require.Equal(t, 2, fi.Sent())
	require.NoError(t, err, "reconnect failed after a lost reply; task state: %s", taskState(env, taskID))
	// A reconnect is not a new attempt.
	attempts, err := env.GetRemoteExecutionRedisClient().HGet(ctx, s.redisKeyForTask(taskID), "attemptCount").Result()
	require.NoError(t, err)
	require.Equal(t, "1", attempts)
	require.NoError(t, reconnected.Finalize())
}

// A claim whose replies are lost on every attempt, so the client gives up
// with an error. The claim may still have been recorded. This documents a
// remaining gap; it is not expected to pass with the lease-ID fix alone.
func TestClaim_KnownGap_AllRetriesLost(t *testing.T) {
	env, ctx, fi := getEnvWithFaultyRedis(t)
	fe := newFakeExecutor(ctx, t, env.GetSchedulerClient())
	fe.Register()
	taskID := scheduleTask(ctx, t, env, map[string]string{})
	fe.WaitForTask(taskID)

	fi.Arm(100, redisfault.EOF, []byte(redisAcquireClaim.Hash()))
	_, err := fe.leaseTask(taskID, "" /*=reconnectToken*/)
	t.Logf("claim returned %v after %d attempts (%d replies lost); task state: %s", err, fi.Sent(), fi.Lost(), taskState(env, taskID))
	require.Error(t, err)
	fi.Arm(0, redisfault.EOF)
	ok, otherErr := otherExecutorCanClaim(ctx, t, env, taskID, 5*time.Second)
	require.True(t, ok, "no executor could claim the task for 5s after a claim error (last error: %v); task state: %s", otherErr, taskState(env, taskID))
}
