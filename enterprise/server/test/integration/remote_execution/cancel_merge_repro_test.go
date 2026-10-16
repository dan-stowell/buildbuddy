package remote_execution_test

import (
	"context"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/test/integration/remote_execution/rbetest"
	"github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_handler"
	"github.com/buildbuddy-io/buildbuddy/server/build_event_publisher"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	bespb "github.com/buildbuddy-io/buildbuddy/proto/build_event_stream"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
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

// targetKeys returns the action-merging keys (forward + reverse) that belong to
// the action digest of the given execution ID, plus the execution ID the
// forward key currently points to.
func targetKeys(t *testing.T, rbe *rbetest.Env, op string) ([]string, string) {
	parts := strings.Split(op, "/")
	hash := parts[len(parts)-2]
	var out []string
	pointsTo := ""
	for _, k := range pendingExecutionKeys(t, rbe) {
		if strings.Contains(k, hash) {
			out = append(out, k)
			if strings.HasPrefix(k, "pendingExecution/") {
				pointsTo, _ = rbe.GetRedisClient().HGet(context.Background(), k, "execution-id").Result()
			}
		}
	}
	return out, pointsTo
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

	// Occupy the only executor slot so that the target action stays queued.
	blocker := rbe.ExecuteControlledCommand("blocker", &rbetest.ExecuteControlledOpts{InvocationID: uuid.NewString()})
	blocker.WaitStarted()

	target := &repb.Command{
		Arguments: []string{"sh", "-c", "echo target"},
		Platform: &repb.Platform{Properties: []*repb.Platform_Property{
			{Name: "OSFamily", Value: runtime.GOOS},
			{Name: "Arch", Value: runtime.GOARCH},
		}},
	}
	execute := func(iid string) (*rbetest.Command, string, time.Duration) {
		start := time.Now()
		cmd := rbe.Execute(target, &rbetest.ExecuteOpts{InvocationID: iid})
		op := cmd.WaitAccepted()
		return cmd, op, time.Since(start)
	}

	iid1, interrupt1 := startInvocation(t, bbServer)
	cmd1, op1, d1 := execute(iid1)
	t.Logf("first Execute accepted in %s (op %s)", d1, op1)
	k, p := targetKeys(t, rbe, op1)
	t.Logf("target keys before cancel: %v (forward key -> cancelled op? %v)", k, p == op1)

	interrupt1()
	cmd1.MustBeCancelled()
	t.Logf("cmd1 cancelled (CANCELLED delivered to client)")

	// Give any asynchronous cleanup a chance to run.
	time.Sleep(3 * time.Second)
	keysAfter, pointsTo := targetKeys(t, rbe, op1)
	t.Logf("target keys 3s after cancel: %v (forward key -> cancelled op? %v)", keysAfter, pointsTo == op1)

	iid2, _ := startInvocation(t, bbServer)
	cmd2, op2, d2 := execute(iid2)
	t.Logf("rerun #1 Execute accepted in %s (op %s, same as cancelled op: %v)", d2, op2, op2 == op1)
	_, p = targetKeys(t, rbe, op1)
	t.Logf("after rerun #1: forward key still -> cancelled op? %v", p == op1)

	// A second identical rerun while rerun #1 is still queued: should merge into
	// rerun #1's execution promptly.
	iid3, _ := startInvocation(t, bbServer)
	cmd3, op3, d3 := execute(iid3)
	t.Logf("rerun #2 Execute accepted in %s (op %s, merged into rerun #1: %v)", d3, op3, op3 == op2)

	// Let things finish so shutdown is clean.
	blocker.Exit(0)
	r2 := cmd2.Wait()
	r3 := cmd3.Wait()
	t.Logf("rerun results: exit %d / %d", r2.ExitCode, r3.ExitCode)

	require.Empty(t, keysAfter, "action-merging keys survived the cancellation")
	require.Less(t, d2, 2*time.Second, "rerun of a cancelled (queued) action was delayed by %s", d2)
	require.Less(t, d3, 2*time.Second, "second rerun was delayed by %s", d3)
	require.Equal(t, op2, op3, "identical concurrent reruns were not merged")
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

	cancelTime := time.Now()
	interrupt1()
	cmd1.MustBeCancelled()
	t.Logf("cmd1 cancelled; keys: %v", pendingExecutionKeys(t, rbe))
	cmd1.WaitDisconnected()
	t.Logf("cmd1 process terminated on executor %s after cancel; keys: %v", time.Since(cancelTime), pendingExecutionKeys(t, rbe))

	// Poll for executor-side / PublishOperation cleanup.
	var keysAfter []string
	for i := range 30 {
		keysAfter, _ = targetKeys(t, rbe, op1)
		if len(keysAfter) == 0 {
			t.Logf("target keys gone %s after cancel (poll iteration %d)", time.Since(cancelTime), i)
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
	time.Sleep(3 * time.Second) // let the new "target" process register with the controller
	cmd2.Exit(0)

	require.Empty(t, keysAfter, "action-merging keys survived the cancellation")
	require.Less(t, d2, 2*time.Second, "rerun of a cancelled (running) action was delayed by %s", d2)
}
