package remote_execution_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/test/integration/remote_execution/rbetest"
	"github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_handler"
	"github.com/buildbuddy-io/buildbuddy/server/build_event_publisher"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	bespb "github.com/buildbuddy-io/buildbuddy/proto/build_event_stream"
)

// Independent reproducer for "Cancel() leaves stale action-merging state".
// Uses only real components: real app (execution server + scheduler server),
// real (test) redis, real executor, and cancellation through the real
// user-facing path (Bazel BEP Finished event with exit code INTERRUPTED).

func pendingExecutionKeys(t *testing.T, rbe *rbetest.Env) []string {
	keys, err := rbe.GetRedisClient().Keys(context.Background(), "pendingExecution*").Result()
	require.NoError(t, err)
	sort.Strings(keys)
	return keys
}

// startInvocation starts a BEP stream for a fresh invocation and returns a
// func that sends Finished(INTERRUPTED), i.e. what Bazel sends on Ctrl-C.
func startInvocation(t *testing.T, bbServer *rbetest.BuildBuddyServer) (string, func()) {
	iid := uuid.NewString()
	bep, err := build_event_publisher.New(bbServer.PublishBuildEventClient(), "", iid)
	require.NoError(t, err)
	bep.Start(context.Background())
	require.NoError(t, bep.Publish(&bespb.BuildEvent{
		Payload: &bespb.BuildEvent_Started{Started: &bespb.BuildStarted{StartTime: timestamppb.Now()}},
	}))
	return iid, func() {
		require.NoError(t, bep.Publish(&bespb.BuildEvent{
			Payload: &bespb.BuildEvent_Finished{Finished: &bespb.BuildFinished{
				ExitCode:   &bespb.BuildFinished_ExitCode{Name: "INTERRUPTED", Code: build_event_handler.InterruptedExitCode},
				FinishTime: timestamppb.Now(),
			}},
		}))
	}
}

func timeToAccept(t *testing.T, cmd *rbetest.ControlledCommand, start time.Time) (string, time.Duration) {
	op := cmd.WaitAccepted()
	return op, time.Since(start)
}

// Case 1: the cancelled action was still QUEUED (no executor capacity).
func TestCancelMergeRepro_QueuedTask(t *testing.T) {
	rbe := rbetest.NewRBETestEnv(t)
	bbServer := rbe.AddBuildBuddyServer()
	rbe.AddSingleTaskExecutor(t)

	// Occupy the only executor slot so that "target" stays queued.
	blocker := rbe.ExecuteControlledCommand("blocker", &rbetest.ExecuteControlledOpts{InvocationID: uuid.NewString()})
	blocker.WaitStarted()

	iid1, interrupt1 := startInvocation(t, bbServer)
	start := time.Now()
	cmd1 := rbe.ExecuteControlledCommand("target", &rbetest.ExecuteControlledOpts{InvocationID: iid1})
	op1, d1 := timeToAccept(t, cmd1, start)
	t.Logf("first Execute accepted in %s (op %s)", d1, op1)
	t.Logf("keys before cancel: %v", pendingExecutionKeys(t, rbe))

	interrupt1()
	cmd1.MustBeCancelled()
	t.Logf("cmd1 cancelled")

	// Give any asynchronous cleanup a chance to run.
	time.Sleep(3 * time.Second)
	keysAfter := pendingExecutionKeys(t, rbe)
	t.Logf("keys 3s after cancel: %v", keysAfter)

	iid2, _ := startInvocation(t, bbServer)
	start = time.Now()
	cmd2 := rbe.ExecuteControlledCommand("target", &rbetest.ExecuteControlledOpts{InvocationID: iid2})
	op2, d2 := timeToAccept(t, cmd2, start)
	t.Logf("rerun Execute accepted in %s (op %s, same as cancelled op: %v)", d2, op2, op2 == op1)
	t.Logf("keys after rerun: %v", pendingExecutionKeys(t, rbe))

	// Let things finish so shutdown is clean.
	blocker.Exit(0)
	cmd2.WaitStarted()
	cmd2.Exit(0)

	require.Empty(t, keysAfter, "action-merging keys survived the cancellation")
	require.Less(t, d2, 2*time.Second, "rerun of a cancelled (queued) action was delayed by %s", d2)
}

// Case 2: the cancelled action was RUNNING on an executor.
func TestCancelMergeRepro_RunningTask(t *testing.T) {
	rbe := rbetest.NewRBETestEnv(t)
	bbServer := rbe.AddBuildBuddyServer()
	rbe.AddExecutor(t)

	iid1, interrupt1 := startInvocation(t, bbServer)
	start := time.Now()
	cmd1 := rbe.ExecuteControlledCommand("target", &rbetest.ExecuteControlledOpts{InvocationID: iid1})
	op1, d1 := timeToAccept(t, cmd1, start)
	cmd1.WaitStarted()
	t.Logf("first Execute accepted in %s (op %s)", d1, op1)
	t.Logf("keys while running: %v", pendingExecutionKeys(t, rbe))

	interrupt1()
	cmd1.MustBeCancelled()
	t.Logf("cmd1 cancelled; keys: %v", pendingExecutionKeys(t, rbe))
	cmd1.WaitDisconnected()
	t.Logf("cmd1 process terminated on executor; keys: %v", pendingExecutionKeys(t, rbe))

	// Poll for executor-side / PublishOperation cleanup.
	var keysAfter []string
	for i := 0; i < 30; i++ {
		keysAfter = pendingExecutionKeys(t, rbe)
		if len(keysAfter) == 0 {
			t.Logf("keys cleaned up %s after process termination", time.Duration(i)*500*time.Millisecond)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("keys after polling: %v", keysAfter)

	iid2, _ := startInvocation(t, bbServer)
	start = time.Now()
	cmd2 := rbe.ExecuteControlledCommand("target", &rbetest.ExecuteControlledOpts{InvocationID: iid2})
	op2, d2 := timeToAccept(t, cmd2, start)
	t.Logf("rerun Execute accepted in %s (op %s, same as cancelled op: %v)", d2, op2, op2 == op1)
	cmd2.WaitStarted()
	cmd2.Exit(0)

	require.Empty(t, keysAfter, "action-merging keys survived the cancellation")
	require.Less(t, d2, 2*time.Second, "rerun of a cancelled (running) action was delayed by %s", d2)
}
