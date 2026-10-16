package hit_tracker_client

import (
	"context"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	hitpb "github.com/buildbuddy-io/buildbuddy/proto/hit_tracker"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// hangingHitTracker blocks every Track RPC until the test releases it.
type hangingHitTracker struct {
	started chan struct{}
	once    sync.Once
	release chan struct{}
}

func (h *hangingHitTracker) Track(ctx context.Context, req *hitpb.TrackRequest) (*hitpb.TrackResponse, error) {
	h.once.Do(func() { close(h.started) })
	<-h.release
	return &hitpb.TrackResponse{}, nil
}

func countHitTrackerWorkers() int {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, "hit_tracker_client.(*HitTrackerFactory)") {
			n++
		}
	}
	return n
}

// A Track RPC that never completes (a remote that accepts the request but
// does not answer) must not keep the sender goroutine alive, and block the
// server's shutdown, past max_shutdown_duration.
func TestShutdownWithHangingTrackRPC(t *testing.T) {
	flags.Set(t, "max_shutdown_duration", 2*time.Second)
	flags.Set(t, "cache_proxy.remote_hit_tracker.workers", 3)
	flags.Set(t, "cache_proxy.remote_hit_tracker.update_interval", 100*time.Millisecond)

	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	svc := &hangingHitTracker{started: make(chan struct{}), release: make(chan struct{})}
	hitpb.RegisterHitTrackerServiceServer(srv, svc)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	flags.Set(t, "cache_proxy.remote_cache", "grpc://"+lis.Addr().String())

	te := testenv.GetTestEnv(t)
	// Cleanups run last-in first-out: unblock the handlers before the test
	// env waits for its health checker to shut down.
	t.Cleanup(func() { close(svc.release) })
	authenticator := testauth.NewTestAuthenticator(t, testauth.TestUsers(user1, group1))
	te.SetAuthenticator(authenticator)
	require.NoError(t, Register(te))

	ctx := authenticatedContext(user1, authenticator)
	ht := te.GetHitTrackerFactory().NewCASHitTracker(ctx, &repb.RequestMetadata{})
	ht.TrackDownload(aDigest).CloseWithBytesTransferred(1, 2, repb.Compressor_IDENTITY, "test")
	select {
	case <-svc.started:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "Track RPC was never sent")
	}

	hc := te.GetHealthChecker()
	hc.Shutdown()
	shutdownDone := make(chan struct{})
	go func() {
		hc.WaitForGracefulShutdown()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "server shutdown blocked on a hanging hit-tracker Track RPC", "hit tracker goroutines still running: %d", countHitTrackerWorkers())
	}
	n := countHitTrackerWorkers()
	for deadline := time.Now().Add(5 * time.Second); n > 0 && time.Now().Before(deadline); n = countHitTrackerWorkers() {
		time.Sleep(50 * time.Millisecond)
	}
	require.Zero(t, n, "hit tracker goroutines still running after shutdown")
}
