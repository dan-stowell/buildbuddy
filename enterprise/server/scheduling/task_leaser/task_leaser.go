package task_leaser

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/environment"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/util/authutil"
	"github.com/buildbuddy-io/buildbuddy/server/util/flag"
	"github.com/buildbuddy-io/buildbuddy/server/util/log"
	"github.com/buildbuddy-io/buildbuddy/server/util/proto"
	"github.com/buildbuddy-io/buildbuddy/server/util/retry"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	scpb "github.com/buildbuddy-io/buildbuddy/proto/scheduler"
	gstatus "google.golang.org/grpc/status"
)

var (
	enableReconnect = flag.Bool("executor.enable_lease_reconnect", true, "Enable task lease reconnection on scheduler server shutdown.")
)

const (
	// Timeout for reconnecting a lease after disconnecting from the server.
	reconnectTimeout = 1 * time.Second

	// Timeout for sending the final lease message on a new stream, if the
	// stream broke before the lease could be closed.
	closeReconnectTimeout = 10 * time.Second
)

// Verify that TaskLeaser implements interfaces.TaskLeaser.
var _ interfaces.TaskLeaser = (*TaskLeaser)(nil)

// Verify that TaskLease implements interfaces.TaskLease.
var _ interfaces.TaskLease = (*TaskLease)(nil)

type TaskLeaser struct {
	env                     environment.Env
	executorID              string
	executorHostname        string
	supportsExperimentFlags bool
}

func NewTaskLeaser(env environment.Env, executorID, executorHostname string, supportsExperimentFlags bool) *TaskLeaser {
	return &TaskLeaser{
		env:                     env,
		executorID:              executorID,
		executorHostname:        executorHostname,
		supportsExperimentFlags: supportsExperimentFlags,
	}
}

func (t *TaskLeaser) Lease(ctx context.Context, taskID string) (interfaces.TaskLease, error) {
	lease := &TaskLease{
		env:                     t.env,
		executorID:              t.executorID,
		executorHostname:        t.executorHostname,
		supportsExperimentFlags: t.supportsExperimentFlags,
		taskID:                  taskID,
		execTask:                &repb.ExecutionTask{},
		quit:                    make(chan struct{}),
		ttl:                     100 * time.Second,
	}
	ctx, serializedTask, err := lease.claim(ctx)
	if err != nil {
		return nil, err
	}
	lease.ctx = ctx
	if err := proto.Unmarshal(serializedTask, lease.execTask); err != nil {
		lease.Close(ctx, nil, false /*=retry*/)
		return nil, status.InternalErrorf("unmarshal ExecutionTask: %s", err)
	}
	return lease, nil
}

type TaskLease struct {
	env                     environment.Env
	executorID              string
	executorHostname        string
	supportsExperimentFlags bool
	taskID                  string

	ctx            context.Context
	execTask       *repb.ExecutionTask
	leaseID        string
	reconnectToken string
	quit           chan struct{}
	mu             sync.Mutex // protects stream
	stream         scpb.Scheduler_LeaseTaskClient
	ttl            time.Duration
	cancelFunc     context.CancelFunc
}

func (t *TaskLease) Context() context.Context {
	return t.ctx
}

func (t *TaskLease) Task() *repb.ExecutionTask {
	return t.execTask
}

func (t *TaskLease) sendRequest(req *scpb.LeaseTaskRequest) (*scpb.LeaseTaskResponse, error) {
	if err := t.stream.Send(req); err != nil {
		if err == io.EOF {
			// Send returns io.EOF if the stream was terminated (e.g. the
			// server was hard-stopped). The actual status can only be
			// obtained from Recv.
			if _, recvErr := t.stream.Recv(); recvErr != nil && recvErr != io.EOF {
				return nil, recvErr
			}
		}
		return nil, err
	}
	return t.stream.Recv()
}

// isReconnectable returns whether a lease stream error indicates that the
// lease should be re-established on a new stream using the reconnect token.
func isReconnectable(err error) bool {
	return *enableReconnect && (status.IsUnavailableError(err) || status.IsInternalError(err))
}

func (t *TaskLease) pingServer(ctx context.Context) (b []byte, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed() {
		// Don't try to send keepalive pings after Close() was called.
		return nil, nil
	}

	req := &scpb.LeaseTaskRequest{
		ExecutorId:              t.executorID,
		ExecutorHostname:        t.executorHostname,
		TaskId:                  t.taskID,
		SupportsReconnect:       *enableReconnect,
		ReconnectToken:          t.reconnectToken,
		SupportsExperimentFlags: t.supportsExperimentFlags,
	}
	var rsp *scpb.LeaseTaskResponse
	var r *retry.Retry
	for {
		var err error
		rsp, err = t.sendRequest(req)
		if err == nil {
			break
		}
		if !isReconnectable(err) {
			return nil, err
		}
		originalErr := err
		// Server is unavailable (e.g. shutting down); retry. Note that we don't
		// start the retry context timeout until after observing the disconnect
		// error.
		stream, err := t.env.GetSchedulerClient().LeaseTask(ctx)
		if err != nil {
			return nil, status.WrapError(err, "reconnect lease")
		}
		t.stream = stream
		if r == nil {
			ctx, cancel := context.WithTimeout(ctx, reconnectTimeout)
			defer cancel()
			r = retry.DefaultWithContext(ctx)
		}
		if !r.Next() {
			return nil, originalErr
		}
	}
	if rsp.GetReconnectToken() != "" {
		t.reconnectToken = rsp.GetReconnectToken()
	}
	if rsp.GetLeaseId() != "" {
		t.leaseID = rsp.GetLeaseId()
	}
	t.ttl = time.Duration(rsp.GetLeaseDurationSeconds()) * time.Second
	return rsp.GetSerializedTask(), nil
}

func (t *TaskLease) reEnqueueTask(ctx context.Context, reason string) error {
	req := &scpb.ReEnqueueTaskRequest{
		TaskId:  t.taskID,
		LeaseId: t.leaseID,
		Reason:  reason,
	}
	_, err := t.env.GetSchedulerClient().ReEnqueueTask(ctx, req)
	return err
}

func (t *TaskLease) keepLease(ctx context.Context) {
	go func() {
		for {
			select {
			case <-t.quit:
				return
			case <-time.After(t.ttl):
				if _, err := t.pingServer(ctx); err != nil {
					log.CtxErrorf(ctx, "Error updating lease for task: %q: %s", t.taskID, err)
					t.cancelFunc()
					return
				}
			}
		}
	}()
}

func (t *TaskLease) claim(ctx context.Context) (context.Context, []byte, error) {
	if t.env.GetSchedulerClient() == nil {
		return nil, nil, status.FailedPreconditionError("Scheduler client not configured")
	}
	stream, err := t.env.GetSchedulerClient().LeaseTask(ctx)
	if err != nil {
		return nil, nil, err
	}
	t.stream = stream
	serializedTask, err := t.pingServer(ctx)
	if err == nil {
		defer t.keepLease(ctx)
		log.CtxDebugf(ctx, "Worker leased task: %q", t.taskID)
	}
	ctx, cancel := context.WithCancel(ctx)
	t.cancelFunc = cancel
	return ctx, serializedTask, err
}

func (t *TaskLease) closed() bool {
	select {
	case <-t.quit:
		return true
	default:
		return false
	}
}

func (t *TaskLease) Close(ctx context.Context, taskErr error, retry bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	log.CtxDebugf(ctx, "Closing task lease")
	if t.closed() {
		log.CtxInfof(ctx, "Lease was already closed. Short-circuiting.")
		return
	}
	close(t.quit) // This cancels our lease-keep-alive background goroutine.

	req := &scpb.LeaseTaskRequest{
		ExecutorId:       t.executorID,
		ExecutorHostname: t.executorHostname,
		TaskId:           t.taskID,
		// The reconnect fields are only used if the final message has to be
		// sent on a new stream (see below).
		SupportsReconnect:       *enableReconnect,
		ReconnectToken:          t.reconnectToken,
		SupportsExperimentFlags: t.supportsExperimentFlags,
	}

	// We can finalize the task if the execution was successful, or if it failed
	// and we're not going to retry it.
	// Otherwise, we should let the scheduler know that the task needs to be
	// retried.
	if taskErr == nil || !retry {
		req.Finalize = true
	} else {
		req.ReEnqueue = true
		s, _ := gstatus.FromError(taskErr)
		req.ReEnqueueReason = s.Proto()
	}

	// The lease context may already be canceled at this point, so use a
	// background context (preserving auth) for any new RPCs.
	bgCtx := context.Background()
	if jwt := t.ctx.Value(authutil.ContextTokenStringKey); jwt != nil {
		bgCtx = context.WithValue(bgCtx, authutil.ContextTokenStringKey, jwt)
	}

	closedCleanly, err := t.sendFinalRequest(req)
	if !closedCleanly && isReconnectable(err) && t.reconnectToken != "" {
		// The lease stream broke (e.g. the server was shut down) before the
		// final message was processed, but the scheduler holds the task for
		// us to reconnect: send the final message on a new stream.
		log.CtxInfof(ctx, "Lease stream failed while closing (%s); reconnecting to send final message", err)
		closedCleanly, err = t.reconnectAndSendFinalRequest(bgCtx, req, err)
	}
	if err != nil {
		log.CtxWarningf(ctx, "Failed to close task lease stream: %s", err)
	}

	if !closedCleanly {
		log.CtxWarningf(ctx, "Lease did not close cleanly; re-enqueueing task")
		reason := ""
		if taskErr != nil {
			reason = taskErr.Error()
		}
		if err := t.reEnqueueTask(bgCtx, reason); err != nil {
			log.CtxWarningf(ctx, "Error re-enqueueing task: %s", err)
		} else {
			log.CtxInfof(ctx, "Successfully re-enqueued task")
		}
	} else {
		log.CtxDebugf(ctx, "Task lease closed cleanly")
	}
}

// sendFinalRequest sends the final message on the lease stream and waits for
// the server to end the stream. It returns whether the server reported that
// the lease was closed cleanly, and the stream error, if any.
func (t *TaskLease) sendFinalRequest(req *scpb.LeaseTaskRequest) (closedCleanly bool, err error) {
	// If Send fails, the stream is done and Recv returns its actual status.
	_ = t.stream.Send(req)
	for {
		rsp, err := t.stream.Recv()
		if err == io.EOF {
			return closedCleanly, nil
		}
		if err != nil {
			return false, err
		}
		closedCleanly = rsp.GetClosedCleanly()
	}
}

// reconnectAndSendFinalRequest re-establishes the lease on a new stream using
// the reconnect token, and sends the final message on it. err is the error
// from the previous attempt.
func (t *TaskLease) reconnectAndSendFinalRequest(ctx context.Context, req *scpb.LeaseTaskRequest, err error) (closedCleanly bool, _ error) {
	ctx, cancel := context.WithTimeout(ctx, closeReconnectTimeout)
	defer cancel()
	retryCtx, cancelRetry := context.WithTimeout(ctx, reconnectTimeout)
	defer cancelRetry()
	r := retry.DefaultWithContext(retryCtx)
	for !closedCleanly && isReconnectable(err) && r.Next() {
		var stream scpb.Scheduler_LeaseTaskClient
		stream, err = t.env.GetSchedulerClient().LeaseTask(ctx)
		if err != nil {
			continue
		}
		t.stream = stream
		closedCleanly, err = t.sendFinalRequest(req)
	}
	return closedCleanly, err
}
