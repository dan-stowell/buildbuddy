package batch_operator_test

// V1 independent reproducer (diagnostic; not intended for upstream).

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/batch_operator"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/real_environment"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testhealthcheck"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

type v1HC struct {
	testhealthcheck.TestingHealthChecker
	fns []interfaces.CheckerFunc
}

func (h *v1HC) RegisterShutdownFunction(f interfaces.CheckerFunc) { h.fns = append(h.fns, f) }

func (h *v1HC) shutdown(ctx context.Context) {
	for _, f := range h.fns {
		f(ctx)
	}
}

type v1Op struct {
	ignoreCtx      bool
	entered        chan struct{}
	release        chan struct{}
	inflight       atomic.Int64
	observedCancel atomic.Int64
	returned       atomic.Int64
}

func newV1Op(ignoreCtx bool) *v1Op {
	return &v1Op{ignoreCtx: ignoreCtx, entered: make(chan struct{}, 1000), release: make(chan struct{})}
}

func (o *v1Op) fn(ctx context.Context, groupID string, b *batch_operator.DigestBatch) error {
	o.inflight.Add(1)
	defer o.inflight.Add(-1)
	defer o.returned.Add(1)
	o.entered <- struct{}{}
	if o.ignoreCtx {
		<-o.release
		return nil
	}
	select {
	case <-ctx.Done():
		o.observedCancel.Add(1)
		return ctx.Err()
	case <-o.release:
		return nil
	}
}

func v1Env(t *testing.T) (*real_environment.RealEnv, context.Context) {
	env := real_environment.NewRealEnv(&testhealthcheck.TestingHealthChecker{})
	a := testauth.NewTestAuthenticator(t, testauth.TestUsers(user1, group1))
	env.SetAuthenticator(a)
	return env, authenticatedContext(context.Background(), user1, a)
}

// startInflight starts an operator and returns once the operator callback is
// blocked inside the sender's flush.
func startInflight(t *testing.T, env *real_environment.RealEnv, ctx context.Context, op *v1Op) *v1HC {
	clock := clockwork.NewFakeClock()
	env.SetClock(clock)
	o, err := batch_operator.New(env, "v1", op.fn, batch_operator.BatchDigestOperatorConfig{BatchInterval: time.Second})
	require.NoError(t, err)
	require.True(t, o.Enqueue(ctx, "", []*repb.Digest{digest0}, repb.DigestFunction_SHA256))
	o.ForceBatchingForTesting() // deterministic: batch is pending before the sender starts
	hc := &v1HC{}
	o.Start(hc)
	clock.Advance(time.Second)
	select {
	case <-op.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("callback never entered")
	}
	return hc
}

func countStacks(substr string) (int, string) {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	var sample string
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, substr) {
			if n == 0 {
				sample = g
			}
			n++
		}
	}
	return n, sample
}

// pollSenders polls (up to 2s) for sender goroutines to go away.
func pollSenders() (int, string) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, s := countStacks("batch_operator.(*batchOperator).sender")
		if n == 0 || time.Now().After(deadline) {
			return n, s
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func runV1(t *testing.T, n int, ignoreCtx bool) {
	env, ctx := v1Env(t)
	op := newV1Op(ignoreCtx)
	base, _ := countStacks("batch_operator.(*batchOperator).sender")
	hcs := make([]*v1HC, n)
	for i := range n {
		hcs[i] = startInflight(t, env, ctx, op)
	}
	// Shut every operator down, with a bound so a hang is observable.
	shutdownDone := make(chan struct{})
	var returnedShutdowns atomic.Int64
	go func() {
		defer close(shutdownDone)
		for _, hc := range hcs {
			sctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			hc.shutdown(sctx)
			cancel()
			returnedShutdowns.Add(1)
		}
	}()
	select {
	case <-shutdownDone:
	case <-time.After(3 * time.Second):
	}
	senders, sample := pollSenders()
	senders -= base
	t.Logf("V1RESULT n=%d ignoreCtx=%v shutdowns_returned=%d/%d sender_goroutines=%d callbacks_inflight=%d callbacks_returned=%d observed_cancel=%d",
		n, ignoreCtx, returnedShutdowns.Load(), n, senders, op.inflight.Load(), op.returned.Load(), op.observedCancel.Load())
	if senders > 0 {
		t.Logf("sample stuck sender stack:\n%s", sample)
	}
	// Release everything so the test binary is clean for subsequent subtests.
	close(op.release)
	<-shutdownDone
	after, _ := pollSenders()
	t.Logf("V1RESULT n=%d after_release sender_goroutines=%d callbacks_inflight=%d", n, after-base, op.inflight.Load())
	require.Equal(t, 0, senders, "sender goroutines survived shutdown")
	require.Equal(t, int64(n), returnedShutdowns.Load(), "shutdown did not return within 3s")
}

func TestV1ShutdownLeak(t *testing.T) {
	for _, n := range []int{1, 10, 50} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) { runV1(t, n, false) })
	}
}

func TestV1ShutdownLeakIgnoreCtx(t *testing.T) {
	for _, n := range []int{1, 10} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) { runV1(t, n, true) })
	}
}

// Detector validation: blocking callback, testleak must catch the stuck sender.
func TestV1Testleak_Blocked(t *testing.T) {
	testleak.Check(t)
	env, ctx := v1Env(t)
	op := newV1Op(false)
	hc := startInflight(t, env, ctx, op)
	hc.shutdown(context.Background())
}

// Detector validation: callback returns promptly before shutdown; testleak
// must pass on baseline.
func TestV1Testleak_Clean(t *testing.T) {
	testleak.Check(t)
	env, ctx := v1Env(t)
	op := newV1Op(false)
	hc := startInflight(t, env, ctx, op)
	close(op.release)
	for op.returned.Load() == 0 {
		runtime.Gosched()
	}
	hc.shutdown(context.Background())
}

// Detector validation with a ctx-ignoring callback that is released only
// after testleak's check: should be caught on both baseline and fix (the fix
// variant hangs in shutdown, so shutdown runs in a goroutine here).
func TestV1Testleak_IgnoreCtx(t *testing.T) {
	op := newV1Op(true)
	t.Cleanup(func() { close(op.release) }) // runs after the leak check
	testleak.Check(t)
	env, ctx := v1Env(t)
	hc := startInflight(t, env, ctx, op)
	done := make(chan struct{})
	go func() { hc.shutdown(context.Background()); close(done) }()
	select {
	case <-done:
		t.Log("shutdown returned")
	case <-time.After(2 * time.Second):
		t.Log("shutdown did not return within 2s")
	}
}
