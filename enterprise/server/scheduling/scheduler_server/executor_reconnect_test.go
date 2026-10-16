package scheduler_server

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/environment"
	"github.com/buildbuddy-io/buildbuddy/server/util/log"
	"github.com/buildbuddy-io/buildbuddy/server/util/proto"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/require"

	scpb "github.com/buildbuddy-io/buildbuddy/proto/scheduler"
)

// scriptedExecutorStream is a server-side RegisterAndStreamWork stream whose
// executor side is driven by the test. Messages the scheduler sends are
// buffered (like bytes accepted into a socket buffer whose peer is gone), and
// nothing is ever answered unless the test does so explicitly.
type scriptedExecutorStream struct {
	scpb.Scheduler_RegisterAndStreamWorkServer
	ctx    context.Context
	cancel context.CancelFunc
	recv   chan *scpb.RegisterAndStreamWorkRequest
	sent   chan *scpb.RegisterAndStreamWorkResponse
	done   chan error
}

func (s *scriptedExecutorStream) Context() context.Context { return s.ctx }

func (s *scriptedExecutorStream) Send(rsp *scpb.RegisterAndStreamWorkResponse) error {
	select {
	case s.sent <- rsp:
		return nil
	case <-s.ctx.Done():
		return status.UnavailableError("transport is closing")
	}
}

func (s *scriptedExecutorStream) Recv() (*scpb.RegisterAndStreamWorkRequest, error) {
	select {
	case req, ok := <-s.recv:
		if !ok {
			return nil, io.EOF
		}
		return req, nil
	case <-s.ctx.Done():
		return nil, status.UnavailableError("transport is closing")
	}
}

// openScriptedStream starts a RegisterAndStreamWork handler on s for a stream
// controlled by the test.
func openScriptedStream(t *testing.T, s *SchedulerServer) *scriptedExecutorStream {
	ctx, cancel := context.WithCancel(context.Background())
	st := &scriptedExecutorStream{
		ctx:    ctx,
		cancel: cancel,
		recv:   make(chan *scpb.RegisterAndStreamWorkRequest),
		sent:   make(chan *scpb.RegisterAndStreamWorkResponse, 1000),
		done:   make(chan error, 1),
	}
	go func() { st.done <- s.RegisterAndStreamWork(st) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-st.done:
		case <-time.After(20 * time.Second):
		}
	})
	return st
}

func (s *scriptedExecutorStream) send(t *testing.T, req *scpb.RegisterAndStreamWorkRequest) {
	select {
	case s.recv <- req:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "scheduler did not read message from stream")
	}
}

func (s *scriptedExecutorStream) register(t *testing.T, node *scpb.ExecutionNode) {
	s.send(t, &scpb.RegisterAndStreamWorkRequest{RegisterExecutorRequest: &scpb.RegisterExecutorRequest{Node: node}})
}

// kill simulates the scheduler finally noticing that the stream's transport
// is dead, and waits for the handler (including its cleanup) to return.
func (s *scriptedExecutorStream) kill(t *testing.T) {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(20 * time.Second):
		require.FailNow(t, "stream handler did not return")
	}
}

func reconnectTestNode(id string, milliCPU int64) *scpb.ExecutionNode {
	return &scpb.ExecutionNode{
		ExecutorId:            id,
		OsFamily:              defaultOS,
		Arch:                  defaultArch,
		Host:                  "executor-host",
		AssignableMemoryBytes: 64_000_000_000,
		AssignableMilliCpu:    milliCPU,
	}
}

func reconnectTestPoolKey() nodePoolKey {
	return nodePoolKey{os: defaultOS, arch: defaultArch}
}

// registeredSchedulerFor returns the scheduler host:port recorded in Redis for
// the executor, or "" if the executor is not registered in Redis.
func registeredSchedulerFor(t *testing.T, s *SchedulerServer, executorID string) string {
	key := reconnectTestPoolKey()
	b, err := s.rdb.HGet(context.Background(), key.redisPoolKey(), executorID).Bytes()
	if err != nil {
		return ""
	}
	r := &scpb.RegisteredExecutionNode{}
	require.NoError(t, proto.Unmarshal(b, r))
	return r.GetSchedulerHostPort()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			require.FailNow(t, "timed out waiting for "+what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newSecondScheduler(t *testing.T, env environment.Env, port int32) *SchedulerServer {
	s2, err := NewSchedulerServerWithOptions(env, &Options{LocalPortOverride: port})
	require.NoError(t, err)
	return s2
}

// Claim A: the executor reconnects to a different app replica while its old
// stream is still open on the first replica. When the first replica later
// tears down the old stream, it deletes the executor's Redis registration,
// even though that registration now belongs to the new stream.
func TestExecutorReconnect_OldStreamCleanupOnOtherReplicaDeletesNewRegistration(t *testing.T) {
	env, _ := getEnv(t, &schedulerOpts{}, "")
	s1 := env.GetSchedulerService().(*SchedulerServer)
	s2 := newSecondScheduler(t, env, 4242)
	require.NotEqual(t, s1.ownHostPort, s2.ownHostPort)

	oldStream := openScriptedStream(t, s1)
	oldStream.register(t, reconnectTestNode("e1", 32_000))
	waitFor(t, "registration via s1", func() bool { return registeredSchedulerFor(t, s1, "e1") == s1.ownHostPort })

	// Executor reconnects (same executor ID) and lands on s2; s1 has not yet
	// noticed that the old stream is dead.
	newStream := openScriptedStream(t, s2)
	newStream.register(t, reconnectTestNode("e1", 32_000))
	waitFor(t, "registration via s2", func() bool { return registeredSchedulerFor(t, s1, "e1") == s2.ownHostPort })

	// s1 finally notices the old stream is gone.
	oldStream.kill(t)

	// The new stream on s2 is still connected and healthy, so the executor
	// should still be registered (pointing at s2).
	got := registeredSchedulerFor(t, s1, "e1")
	log.Infof("after old-stream cleanup: registration in redis points at %q (s2=%q)", got, s2.ownHostPort)

	// Show the user-visible consequence on a scheduler whose node list is
	// read from Redis: it believes there are no executors.
	req := newScheduleRequest(context.Background(), t, env, scheduleOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, schedErr := s1.ScheduleTask(ctx, req)
	log.Infof("ScheduleTask on s1 after old-stream cleanup: %v", schedErr)

	require.Equal(t, s2.ownHostPort, got, "old stream's cleanup on s1 deleted the registration owned by the new stream on s2")
}

// Claim B: the executor reconnects to the same app replica while its old
// stream is still open. The replica keeps routing reservations to the old
// stream's handle, and ignores the new registration's metadata.
func TestExecutorReconnect_SameReplicaKeepsRoutingToOldStream(t *testing.T) {
	env, _ := getEnv(t, &schedulerOpts{}, "")
	s := env.GetSchedulerService().(*SchedulerServer)

	oldStream := openScriptedStream(t, s)
	oldStream.register(t, reconnectTestNode("e1", 32_000))
	waitFor(t, "registration", func() bool { return registeredSchedulerFor(t, s, "e1") == s.ownHostPort })

	// Reconnect with the same ID, advertising different capacity.
	newStream := openScriptedStream(t, s)
	newStream.register(t, reconnectTestNode("e1", 64_000))
	// Wait for the registration to be processed (Redis is updated first).
	waitFor(t, "re-registration in redis", func() bool {
		b, err := s.rdb.HGet(context.Background(), reconnectTestPoolKey().redisPoolKey(), "e1").Bytes()
		if err != nil {
			return false
		}
		r := &scpb.RegisteredExecutionNode{}
		return proto.Unmarshal(b, r) == nil && r.GetRegistration().GetAssignableMilliCpu() == 64_000
	})

	pool, ok := s.getPool(reconnectTestPoolKey())
	require.True(t, ok)
	local := pool.FindConnectedExecutorByID("e1")
	require.NotNil(t, local)
	log.Infof("local routing entry for e1: milliCPU=%d", local.GetAssignableMilliCpu())

	// Schedule a task. With a single (reconnected) executor, its reservation
	// should be delivered on the new stream.
	req := newScheduleRequest(context.Background(), t, env, scheduleOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	schedDone := make(chan error, 1)
	go func() {
		_, err := s.ScheduleTask(ctx, req)
		schedDone <- err
	}()

	var gotOnNew, gotOnOld int
	timeout := time.After(10 * time.Second)
loop:
	for {
		select {
		case rsp := <-newStream.sent:
			if rsp.GetEnqueueTaskReservationRequest().GetTaskId() == req.GetTaskId() {
				gotOnNew++
				// Ack like a real executor would.
				newStream.send(t, &scpb.RegisterAndStreamWorkRequest{EnqueueTaskReservationResponse: &scpb.EnqueueTaskReservationResponse{TaskId: req.GetTaskId()}})
			}
		case rsp := <-oldStream.sent:
			if rsp.GetEnqueueTaskReservationRequest().GetTaskId() == req.GetTaskId() {
				gotOnOld++
			}
		case err := <-schedDone:
			log.Infof("ScheduleTask returned after %s: %v", time.Since(start), err)
			break loop
		case <-timeout:
			break loop
		}
	}
	log.Infof("reservations for %s: new stream=%d, old (dead) stream=%d after %s", req.GetTaskId(), gotOnNew, gotOnOld, time.Since(start))

	require.Equal(t, int64(64_000), local.GetAssignableMilliCpu(), "local routing entry still has the old stream's registration")
	require.Positive(t, gotOnNew, "reservation was never delivered to the reconnected stream (old stream got %d)", gotOnOld)
}

// Consequences of B: measure how the reconnected executor gets work while the
// old stream is still open (work stealing), and when routing recovers after
// the old stream is finally torn down. This test asserts only what currently
// happens so that it documents the recovery path.
func TestExecutorReconnect_SameReplicaRecovery(t *testing.T) {
	env, _ := getEnv(t, &schedulerOpts{}, "")
	s := env.GetSchedulerService().(*SchedulerServer)

	oldStream := openScriptedStream(t, s)
	oldStream.register(t, reconnectTestNode("e1", 32_000))
	waitFor(t, "registration", func() bool { return registeredSchedulerFor(t, s, "e1") == s.ownHostPort })
	newStream := openScriptedStream(t, s)
	newStream.register(t, reconnectTestNode("e1", 32_000))

	req := newScheduleRequest(context.Background(), t, env, scheduleOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	schedDone := make(chan error, 1)
	go func() {
		_, err := s.ScheduleTask(ctx, req)
		schedDone <- err
	}()

	// Let the scheduler push to the dead stream for a while.
	time.Sleep(6 * time.Second)
	oldCount := 0
drain:
	for {
		select {
		case <-oldStream.sent:
			oldCount++
		default:
			break drain
		}
	}
	log.Infof("after 6s: old stream got %d reservations; ScheduleTask still blocked=%v", oldCount, len(schedDone) == 0)

	// Idle executor asks for more work on the new stream (work stealing).
	newStream.send(t, &scpb.RegisterAndStreamWorkRequest{AskForMoreWorkRequest: &scpb.AskForMoreWorkRequest{}})
	stolen := false
	stealDeadline := time.After(5 * time.Second)
steal:
	for {
		select {
		case rsp := <-newStream.sent:
			if rsp.GetEnqueueTaskReservationRequest().GetTaskId() == req.GetTaskId() {
				stolen = true
				break steal
			}
		case <-stealDeadline:
			break steal
		}
	}
	log.Infof("work stealing on the new stream delivered the task: %v (%s after schedule)", stolen, time.Since(start))

	// The old stream's transport is finally detected as dead.
	oldStream.kill(t)
	pool, _ := s.getPool(reconnectTestPoolKey())
	log.Infof("after old-stream cleanup: local entry=%v, redis=%q", pool.FindConnectedExecutorByID("e1") != nil, registeredSchedulerFor(t, s, "e1"))

	// Next periodic registration on the new stream re-adds it.
	newStream.register(t, reconnectTestNode("e1", 32_000))
	waitFor(t, "re-add", func() bool { return pool.FindConnectedExecutorByID("e1") != nil })
	log.Infof("after next heartbeat: local entry=%v, redis=%q", pool.FindConnectedExecutorByID("e1") != nil, registeredSchedulerFor(t, s, "e1"))

	select {
	case err := <-schedDone:
		log.Infof("ScheduleTask returned after %s: %v", time.Since(start), err)
	case <-time.After(15 * time.Second):
		log.Infof("ScheduleTask still blocked %s after start", time.Since(start))
	}
	require.True(t, stolen)
}
