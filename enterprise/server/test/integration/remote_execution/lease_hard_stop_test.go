package remote_execution_test

// Independent reproducer (bug hunt task "repro-lease") for the claim that an
// executor's TaskLease does not reconnect when the app replica holding its
// LeaseTask stream is hard-stopped.
//
// Setup: two real BuildBuddy app replicas (scheduler + execution server) and
// one real executor (real TaskLeaser, PriorityTaskScheduler, executor and
// operation publisher), wired through the rbetest L7 proxy. LeaseTask streams
// are routed to app1; everything else (client Execute, executor registration,
// PublishOperation, CAS) goes to app2, so app1's only long-lived streams are
// lease streams. app1 is stopped through its real HealthChecker, i.e. the
// app's normal shutdown sequence: lameduck sleep, then the shutdown funcs in
// parallel (scheduler shutdown + grpc_server.GRPCShutdown, which calls
// GracefulStop and hard-stops 100ms before max_shutdown_duration expires).
// Nothing in the test calls grpcServer.Stop() directly.
//
// Timing: lease_duration=3s, shutdown_lameduck_duration=300ms,
// max_shutdown_duration=1.5s, so the soft "server is shutting down" window is
// [0.3s, 1.4s] after shutdown starts, shorter than one lease renewal
// interval. The shutdown starts right after the lease's claim, so the
// executor's next renewal (claim+3s) comes after the hard stop (~claim+1.5s):
// this lease is one of the ones that do not renew inside the soft window.

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/operation"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/scheduling/scheduler_server"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/test/integration/remote_execution/rbeclient"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/test/integration/remote_execution/rbetest"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/util/grpc_client"
	"github.com/buildbuddy-io/buildbuddy/server/util/log"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

type hardStopScenario struct {
	// retry is the value of the "retry" platform property ("" = default true).
	retry string
	// finishAfter is how long after app1's shutdown starts the action
	// finishes (if its context has not been canceled before).
	finishAfter time.Duration
	// leaseDuration overrides the scheduler lease duration (default 3s).
	leaseDuration time.Duration
}

type hardStopOutcome struct {
	attempts         int32
	clientResult     *rbeclient.CommandResult
	waitExecResponse *repb.ExecuteResponse
}

func runHardStopScenario(t *testing.T, sc hardStopScenario) *hardStopOutcome {
	flags.Set(t, "shutdown_lameduck_duration", 300*time.Millisecond)
	flags.Set(t, "max_shutdown_duration", 1500*time.Millisecond)

	rbe := rbetest.NewRBETestEnv(t)
	leaseDuration := 3 * time.Second
	if sc.leaseDuration != 0 {
		leaseDuration = sc.leaseDuration
	}
	schedOpts := scheduler_server.Options{LeaseDuration: leaseDuration}
	app1 := rbe.AddBuildBuddyServerWithOptions(&rbetest.BuildBuddyServerOptions{SchedulerServerOptions: schedOpts})
	app2 := rbe.AddBuildBuddyServerWithOptions(&rbetest.BuildBuddyServerOptions{SchedulerServerOptions: schedOpts})

	// "Load balancer": LeaseTask -> app1 while it is ready, everything else
	// (and LeaseTask once app1 is unready) -> app2.
	var app1Ready atomic.Bool
	app1Ready.Store(true)
	rbe.AppProxy.SetDirector(func(ctx context.Context, fullMethodName string) (context.Context, *grpc.ClientConn, error) {
		target := app2.GRPCAddress()
		if app1Ready.Load() && fullMethodName == "/scheduler.Scheduler/LeaseTask" {
			target = app1.GRPCAddress()
		}
		log.Debugf("repro-lease proxy: %s -> %s", fullMethodName, target)
		conn, err := grpc_client.DialSimpleWithoutPooling(target)
		if err != nil {
			return nil, nil, err
		}
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			ctx = metadata.NewOutgoingContext(ctx, md.Copy())
		}
		return ctx, conn, nil
	})

	var attempts atomic.Int32
	started := make(chan struct{}, 10)
	finish := make(chan struct{})
	rbe.AddExecutorWithOptions(t, &rbetest.ExecutorOptions{
		Name: "executor-1",
		RunInterceptor: func(ctx context.Context, _ rbetest.RunFunc) *interfaces.CommandResult {
			n := attempts.Add(1)
			log.Infof("repro-lease: action attempt %d started", n)
			started <- struct{}{}
			select {
			case <-finish:
				log.Infof("repro-lease: action attempt %d finished successfully", n)
				return &interfaces.CommandResult{ExitCode: 0, Stdout: []byte(fmt.Sprintf("attempt-%d", n))}
			case <-ctx.Done():
				log.Infof("repro-lease: action attempt %d canceled: %s", n, ctx.Err())
				return &interfaces.CommandResult{ExitCode: -1, Error: ctx.Err()}
			}
		},
	})

	props := []*repb.Platform_Property{
		{Name: "container-image", Value: "none"},
		{Name: "OSFamily", Value: runtime.GOOS},
		{Name: "Arch", Value: runtime.GOARCH},
	}
	if sc.retry != "" {
		props = append(props, &repb.Platform_Property{Name: "retry", Value: sc.retry})
	}
	cmd := rbe.Execute(&repb.Command{
		Arguments: []string{"repro-lease", uuid.NewString()},
		Platform:  &repb.Platform{Properties: props},
	}, &rbetest.ExecuteOpts{})

	select {
	case <-started:
	case <-time.After(30 * time.Second):
		require.FailNow(t, "action did not start")
	}

	// Rollout of app1: LB stops sending it new requests, SIGTERM-equivalent.
	shutdownStart := time.Now()
	app1Ready.Store(false)
	log.Infof("repro-lease: starting app1 shutdown (real HealthChecker path)")
	app1.Shutdown()

	time.AfterFunc(sc.finishAfter, func() {
		log.Infof("repro-lease: releasing action %s after shutdown start", time.Since(shutdownStart))
		close(finish)
	})

	res := waitCompleted(t, cmd)
	log.Infof("repro-lease: client got COMPLETED: err=%v exit=%d", res.Err, res.ExitCode)

	// Let the executor finish closing its lease (and any re-enqueue / re-run
	// play out) before observing the final state.
	time.Sleep(5 * time.Second)

	// What a client that (re)connects with WaitExecution sees now, e.g. Bazel
	// after its Execute stream broke during the same rollout.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stream, err := rbe.GetRemoteExecutionClient().WaitExecution(ctx, &repb.WaitExecutionRequest{Name: res.ID})
	require.NoError(t, err)
	var last *repb.ExecuteResponse
	for {
		op, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if operation.ExtractStage(op) == repb.ExecutionStage_COMPLETED {
			last = operation.ExtractExecuteResponse(op)
		}
	}
	log.Infof("repro-lease: WaitExecution after completion: status=%v exit=%d", last.GetStatus(), last.GetResult().GetExitCode())
	return &hardStopOutcome{attempts: attempts.Load(), clientResult: res, waitExecResponse: last}
}

func waitCompleted(t *testing.T, cmd *rbetest.Command) *rbeclient.CommandResult {
	timeout := time.After(60 * time.Second)
	for {
		select {
		case r, ok := <-cmd.StatusChannel():
			require.True(t, ok, "status channel closed without COMPLETED")
			if r.Stage == repb.ExecutionStage_COMPLETED {
				return r
			}
		case <-timeout:
			require.FailNow(t, "command did not complete")
		}
	}
}

func checkSuccess(t *testing.T, o *hardStopOutcome) {
	require.NoError(t, o.clientResult.Err, "client-visible Execute result should be success")
	require.Equal(t, 0, o.clientResult.ExitCode)
	require.NotNil(t, o.waitExecResponse)
	require.Equal(t, int32(0), o.waitExecResponse.GetStatus().GetCode(),
		"WaitExecution after completion should still report the successful result, got status %v", o.waitExecResponse.GetStatus())
	require.Equal(t, int32(1), o.attempts, "action should run exactly once")
}

// A: the action finishes after app1 hard-stopped but before the executor's
// next lease renewal; Close() must Finalize a successful task.
func TestLeaseHardStop_FinishBeforeRenewal_RetryFalse(t *testing.T) {
	checkSuccess(t, runHardStopScenario(t, hardStopScenario{retry: "false", finishAfter: 2 * time.Second}))
}

func TestLeaseHardStop_FinishBeforeRenewal_RetryDefault(t *testing.T) {
	checkSuccess(t, runHardStopScenario(t, hardStopScenario{finishAfter: 2 * time.Second}))
}

// B: the action is still running at the executor's next renewal (~claim+3s),
// which is after the hard stop; it finishes at shutdown+5s.
func TestLeaseHardStop_RunsPastRenewal_RetryFalse(t *testing.T) {
	checkSuccess(t, runHardStopScenario(t, hardStopScenario{retry: "false", finishAfter: 5 * time.Second}))
}

func TestLeaseHardStop_RunsPastRenewal_RetryDefault(t *testing.T) {
	checkSuccess(t, runHardStopScenario(t, hardStopScenario{finishAfter: 5 * time.Second}))
}

// Control: the action finishes inside the soft window (before the hard stop).
func TestLeaseHardStop_Control_FinishBeforeHardStop(t *testing.T) {
	checkSuccess(t, runHardStopScenario(t, hardStopScenario{retry: "false", finishAfter: 700 * time.Millisecond}))
}

// C: lease_duration=5s, so the next renewal is at ~claim+5s. The action
// finishes at shutdown+3.5s: after the hard stop (+1.4s) AND after the
// scheduler's 1s lease_reconnect_grace_period has run out (~+2.5s), but
// before the next renewal. Whenever the renewal interval is much longer than
// the reconnect grace period, this is the common case for a lease that missed
// the soft window.
func TestLeaseHardStop_FinishAfterReconnectGrace_RetryFalse(t *testing.T) {
	checkSuccess(t, runHardStopScenario(t, hardStopScenario{retry: "false", finishAfter: 3500 * time.Millisecond, leaseDuration: 5 * time.Second}))
}

func TestLeaseHardStop_FinishAfterReconnectGrace_RetryDefault(t *testing.T) {
	checkSuccess(t, runHardStopScenario(t, hardStopScenario{finishAfter: 3500 * time.Millisecond, leaseDuration: 5 * time.Second}))
}
