package healthcheck_test

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/healthcheck"
	"github.com/stretchr/testify/require"
)

func TestShutdown_StopsBackgroundGoroutines(t *testing.T) {
	testleak.Check(t)

	hc := healthcheck.NewHealthChecker("test")
	hc.Shutdown()
	hc.WaitForGracefulShutdown()
}

func TestSignal_StartsShutdown(t *testing.T) {
	hc := healthcheck.NewHealthChecker("test")

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGTERM))

	done := make(chan struct{})
	go func() {
		hc.WaitForGracefulShutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not finish after SIGTERM")
	}
}

// These tests change process-wide flags and must not run in parallel.
func setShutdownFlag(t *testing.T, name, value string) {
	t.Helper()
	f := flag.Lookup(name)
	require.NotNil(t, f)
	old := f.Value.String()
	require.NoError(t, flag.Set(name, value))
	t.Cleanup(func() { require.NoError(t, flag.Set(name, old)) })
}

func TestShutdown_LameduckReadinessAndCooperativeFlush(t *testing.T) {
	setShutdownFlag(t, "max_shutdown_duration", "5s")
	setShutdownFlag(t, "shutdown_lameduck_duration", "100ms")
	hc := healthcheck.NewHealthChecker("test")
	started := make(chan struct{})
	release := make(chan struct{})
	flushed := make(chan struct{})
	hc.RegisterShutdownFunction(func(ctx context.Context) error {
		close(started)
		<-release
		close(flushed)
		return nil
	})
	readiness := func() int {
		w := httptest.NewRecorder()
		hc.ReadinessHandler().ServeHTTP(w, httptest.NewRequest("GET", "/?server-type=test", nil))
		return w.Code
	}
	require.Equal(t, http.StatusOK, readiness())
	hc.Shutdown()
	// Readiness must turn off before callbacks start, during lameduck.
	require.Eventually(t, func() bool { return readiness() == http.StatusServiceUnavailable }, time.Second, time.Millisecond)
	<-started
	close(release)
	hc.WaitForGracefulShutdown()
	select {
	case <-flushed:
	default:
		t.Fatal("shutdown returned before the cooperative flush completed")
	}
	require.Equal(t, http.StatusServiceUnavailable, readiness())
}

func TestShutdown_CallbackReceivesDeadline(t *testing.T) {
	setShutdownFlag(t, "max_shutdown_duration", "50ms")
	setShutdownFlag(t, "shutdown_lameduck_duration", "0s")
	hc := healthcheck.NewHealthChecker("test")
	expired := make(chan error, 1)
	hc.RegisterShutdownFunction(func(ctx context.Context) error {
		<-ctx.Done()
		expired <- ctx.Err()
		return nil
	})
	hc.Shutdown()
	hc.WaitForGracefulShutdown()
	require.ErrorIs(t, <-expired, context.DeadlineExceeded)
}
