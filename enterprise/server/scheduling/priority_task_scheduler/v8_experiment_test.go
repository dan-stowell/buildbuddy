package priority_task_scheduler

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	scpb "github.com/buildbuddy-io/buildbuddy/proto/scheduler"
)

// ctxBlockingExecutor runs tasks that only finish when their ctx is cancelled.
type ctxBlockingExecutor struct {
	started chan string
}

func (e *ctxBlockingExecutor) ID() string     { return "ctx-blocking" }
func (e *ctxBlockingExecutor) HostID() string { return "ctx-blocking-host" }
func (e *ctxBlockingExecutor) ExecuteTaskAndStreamResults(ctx context.Context, st *repb.ScheduledTask, _ interfaces.Publisher) (bool, error) {
	e.started <- st.GetExecutionTask().GetExecutionId()
	<-ctx.Done()
	return false, ctx.Err()
}

// Exactly the production pattern from Shutdown, with the real clock.
// Returns true if the "hard stop" branch ran.
func shutdownSelectPattern(clock clockwork.Clock, delay time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), delay)
	defer cancel()
	res := make(chan bool, 1)
	go func() {
		select {
		case <-ctx.Done():
			res <- false
		case <-clock.After(delay):
			res <- true
		}
	}()
	// Production blocks in activeTaskCancelFuncs.Wait() here.
	return <-res
}

func TestV8_SelectPatternRate(t *testing.T) {
	clock := clockwork.NewRealClock()
	for _, load := range []int{0, 2 * runtime.GOMAXPROCS(0)} {
		for _, delay := range []time.Duration{-time.Millisecond, 0, 100 * time.Microsecond, time.Millisecond, 5 * time.Millisecond} {
			stop := make(chan struct{})
			var wg sync.WaitGroup
			for range load {
				wg.Go(func() {
					x := 0
					for {
						select {
						case <-stop:
							return
						default:
						}
						for j := range 10000 {
							x += j
						}
						_ = x
					}
				})
			}
			n := 400
			if delay >= time.Millisecond {
				n = 150
			}
			hard := 0
			for range n {
				if shutdownSelectPattern(clock, delay) {
					hard++
				}
			}
			close(stop)
			wg.Wait()
			t.Logf("RESULT pattern GOMAXPROCS=%d load=%d delay=%v: rootCancel(hard-stop) %d/%d, buggy(graceful branch) %d/%d", runtime.GOMAXPROCS(0), load, delay, hard, n, n-hard, n)
		}
	}
}

func runRealClockShutdown(t *testing.T, numTasks int, deadlineIn, cleanup time.Duration) (rootCancelled bool) {
	flags.Set(t, "executor.shutdown_cleanup_duration", cleanup)
	env := testenv.GetTestEnv(t)
	env.SetRemoteExecutionClient(&FakeExecutionClient{})
	exec := &ctxBlockingExecutor{started: make(chan string, numTasks)}
	s, err := NewPriorityTaskScheduler(env, exec, &FakeRunnerPool{}, NewFakeTaskLeaser(), &Options{
		RAMBytesCapacityOverride:  1 << 40,
		CPUMillisCapacityOverride: 1 << 30,
	})
	require.NoError(t, err)
	require.NoError(t, s.Start())
	size := &scpb.TaskSize{EstimatedMilliCpu: 1, EstimatedMemoryBytes: 1}
	for i := range numTasks {
		_, err := s.EnqueueTaskReservation(context.Background(), &scpb.EnqueueTaskReservationRequest{
			TaskId: fakeTaskID(fmt.Sprintf("t%d", i)), TaskSize: size,
			SchedulingMetadata: &scpb.SchedulingMetadata{TaskSize: size},
		})
		require.NoError(t, err)
	}
	for i := range numTasks {
		select {
		case <-exec.started:
		case <-time.After(10 * time.Second):
			t.Fatalf("task %d didn't start", i)
		}
	}
	// Production-like: parent ctx carries a deadline (healthcheck's
	// WithTimeout(max_shutdown_duration)) and is never cancelled early.
	ctx, cancel := context.WithTimeout(context.Background(), deadlineIn)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Shutdown(ctx) }()
	select {
	case <-s.rootContext.Done():
		rootCancelled = true
	case <-time.After(deadlineIn + 2*time.Second):
	}
	if !rootCancelled {
		s.rootCancel() // unblock so the iteration can finish
	}
	require.NoError(t, <-done)
	return rootCancelled
}

func startLoad(n int) func() {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			x := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				for j := range 10000 {
					x += j
				}
				_ = x
			}
		})
	}
	return func() { close(stop); wg.Wait() }
}

func TestV8_RealClockShutdownRate(t *testing.T) {
	for _, load := range []int{0, 2 * runtime.GOMAXPROCS(0)} {
		for _, n := range []int{1, 10, 50} {
			for _, tc := range []struct{ deadline, cleanup time.Duration }{
				{30 * time.Millisecond, 20 * time.Millisecond},  // delay ~ +10ms
				{20 * time.Millisecond, 20 * time.Millisecond},  // delay ~ 0
				{10 * time.Millisecond, 20 * time.Millisecond},  // delay < 0 (max_shutdown < cleanup)
			} {
				stopLoad := startLoad(load)
				iters := 25
				missed := 0
				for i := range iters {
					t.Run(fmt.Sprintf("l%d_n%d_%v_%v_%d", load, n, tc.deadline, tc.cleanup, i), func(t *testing.T) {
						if !runRealClockShutdown(t, n, tc.deadline, tc.cleanup) {
							missed++
						}
					})
				}
				stopLoad()
				t.Logf("RESULT scheduler load=%d N=%d deadline=%v cleanup=%v: rootContext NOT cancelled %d/%d", load, n, tc.deadline, tc.cleanup, missed, iters)
			}
		}
	}
}

func TestV8_SelectPatternLongDelay(t *testing.T) {
	clock := clockwork.NewRealClock()
	for _, load := range []int{0, 2 * runtime.GOMAXPROCS(0)} {
		stopLoad := startLoad(load)
		hard, n := 0, 40
		for range n {
			if shutdownSelectPattern(clock, 100*time.Millisecond) {
				hard++
			}
		}
		stopLoad()
		t.Logf("RESULT pattern load=%d delay=100ms: buggy(graceful branch) %d/%d", load, n-hard, n)
	}
}
