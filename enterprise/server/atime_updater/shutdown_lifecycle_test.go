package atime_updater

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testhealthcheck"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
)

type hangingAtimeCAS struct {
	repb.UnimplementedContentAddressableStorageServer
	active  atomic.Int32
	started chan struct{}
}

func (s *hangingAtimeCAS) FindMissingBlobs(ctx context.Context, req *repb.FindMissingBlobsRequest) (*repb.FindMissingBlobsResponse, error) {
	if req.InstanceName == "warmup" {
		return &repb.FindMissingBlobsResponse{}, nil
	}
	s.active.Add(1)
	defer s.active.Add(-1)
	s.started <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

type atimeLifecycleHealthChecker struct {
	testhealthcheck.TestingHealthChecker
	shutdown interfaces.CheckerFunc
}

func (h *atimeLifecycleHealthChecker) RegisterShutdownFunction(f interfaces.CheckerFunc) {
	h.shutdown = f
}
func atimeLifecycleFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	count := 0
	for _, e := range entries {
		_, err := os.Readlink("/proc/self/fd/" + e.Name())
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		count++
	}
	return count
}

// The gRPC connection and remote server deliberately stay alive until after the
// inner subtest's leak check: closing either would mask missing updater cleanup.
func TestAtimeUpdaterShutdownCancelsInflightRPC(t *testing.T) {
	testleak.Check(t)
	env := testenv.GetTestEnv(t)
	flags.Set(t, "cache_proxy.remote_atime_update_interval", time.Second)
	clock := clockwork.NewFakeClock()
	env.SetClock(clock)
	remote := &hangingAtimeCAS{started: make(chan struct{}, 1)}
	server, run, lis := testenv.RegisterLocalGRPCServer(t, env)
	repb.RegisterContentAddressableStorageServer(server, remote)
	go run()
	conn, err := testenv.LocalGRPCConn(t, context.Background(), lis)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	client := repb.NewContentAddressableStorageClient(conn)
	env.SetContentAddressableStorageClient(client)
	_, err = client.FindMissingBlobs(t.Context(), &repb.FindMissingBlobsRequest{InstanceName: "warmup"})
	require.NoError(t, err)
	t.Run("updaters_closed_transport_open", func(t *testing.T) {
		testleak.Check(t)
		baseG, baseFD := runtime.NumGoroutine(), atimeLifecycleFDCount(t)
		t.Logf("N=0 goroutines=%d FDs=%d remote_active=%d", baseG, baseFD, remote.active.Load())
		done := 0
		for _, n := range []int{1, 10, 50} {
			for done < n {
				updater, err := new(env)
				require.NoError(t, err)
				requestCtx, cancelRequest := context.WithCancel(authenticatedContext(context.Background(), "", env.GetAuthenticator()))
				require.True(t, updater.Enqueue(requestCtx, fmt.Sprintf("request-%d", done), []*repb.Digest{digest0}, repb.DigestFunction_SHA256))
				updater.ForceBatchingForTesting()
				cancelRequest() // Enqueued updates intentionally outlive their source request.
				hc := &atimeLifecycleHealthChecker{}
				updater.Start(hc)
				clock.Advance(time.Second)
				select {
				case <-remote.started:
				case <-time.After(5 * time.Second):
					t.Fatal("background atime RPC never started")
				}
				shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
				require.NoError(t, hc.shutdown(shutdownCtx))
				cancelShutdown()
				done++
			}
			// Give cancellation a chance to reach the remote; keep collecting all N
			// values on baseline rather than stopping after the first leaked updater.
			deadline := time.Now().Add(200 * time.Millisecond)
			ticker := time.NewTicker(time.Millisecond)
			for remote.active.Load() != 0 && time.Now().Before(deadline) {
				<-ticker.C
			}
			ticker.Stop()
			t.Logf("N=%d goroutines=%d FDs=%d remote_active=%d (all updater shutdown callbacks returned)", n, runtime.NumGoroutine(), atimeLifecycleFDCount(t), remote.active.Load())
		}
		require.Eventually(t, func() bool { return remote.active.Load() == 0 }, 5*time.Second, 10*time.Millisecond, "updaters shut down but background remote RPCs are still owned")
	})
}
