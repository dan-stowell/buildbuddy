package hit_tracker_client

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	hitpb "github.com/buildbuddy-io/buildbuddy/proto/hit_tracker"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// i3Goroutines returns the number of goroutines whose stack contains substr.
func i3Goroutines(substr string) (int, string) {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	var sample string
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, substr) {
			n++
			if sample == "" {
				sample = g
			}
		}
	}
	return n, sample
}

func i3WaitGoroutines(substr string, limit int) (int, string) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		n, s := i3Goroutines(substr)
		if n <= limit || time.Now().After(deadline) {
			return n, s
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type i3TrackService struct {
	fail  atomic.Bool
	calls atomic.Int64
	hits  atomic.Int64
}

func (s *i3TrackService) Track(ctx context.Context, req *hitpb.TrackRequest) (*hitpb.TrackResponse, error) {
	s.calls.Add(1)
	if s.fail.Load() {
		return nil, status.UnavailableError("remote failing")
	}
	s.hits.Add(int64(len(req.GetHits())))
	return &hitpb.TrackResponse{}, nil
}

func i3StartTrackServer(t *testing.T) (*i3TrackService, string) {
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	svc := &i3TrackService{}
	hitpb.RegisterHitTrackerServiceServer(srv, svc)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return svc, "grpc://" + lis.Addr().String()
}

// Drives hits through the real Register path and shuts down through the
// health checker, then checks that no hit tracker goroutines remain.
func TestI3_HitTrackerShutdown(t *testing.T) {
	for _, failing := range []bool{false, true} {
		for _, n := range []int{10, 1000, 20000} {
			t.Run(fmt.Sprintf("failing=%v/n=%d", failing, n), func(t *testing.T) {
				svc, target := i3StartTrackServer(t)
				svc.fail.Store(failing)
				flags.Set(t, "cache_proxy.remote_cache", target)
				flags.Set(t, "cache_proxy.remote_hit_tracker.workers", 3)
				flags.Set(t, "cache_proxy.remote_hit_tracker.update_interval", 100*time.Millisecond)
				flags.Set(t, "cache_proxy.remote_hit_tracker.remote_hit_queue_size", 100_000)

				before, _ := i3Goroutines("hit_tracker_client.(*HitTrackerFactory)")
				grpcBefore, _ := i3Goroutines("google.golang.org/grpc")
				te := testenv.GetTestEnv(t)
				authenticator := testauth.NewTestAuthenticator(t, testauth.TestUsers(user1, group1))
				te.SetAuthenticator(authenticator)
				require.NoError(t, Register(te))
				f := te.GetHitTrackerFactory()
				running, _ := i3Goroutines("hit_tracker_client.(*HitTrackerFactory)")

				for i := range n {
					var ctx context.Context
					var cancel context.CancelFunc
					switch i % 3 {
					case 0:
						ctx, cancel = context.WithCancel(authenticatedContext(user1, authenticator))
					case 1:
						ctx, cancel = context.WithCancel(authenticatedContextWithOrigin(user1, authenticator, fmt.Sprintf("origin-%d", i%50)))
					default:
						ctx, cancel = context.WithCancel(context.Background())
					}
					ht := f.NewCASHitTracker(ctx, &repb.RequestMetadata{})
					tt := ht.TrackDownload(aDigest)
					if i%2 == 0 {
						cancel() // request ctx cancelled before the hit is recorded
					}
					tt.CloseWithBytesTransferred(1, 2, repb.Compressor_IDENTITY, "test")
					cancel()
				}
				time.Sleep(300 * time.Millisecond)

				te.GetHealthChecker().Shutdown()
				te.GetHealthChecker().WaitForGracefulShutdown()

				// Enqueue after shutdown takes the synchronous path.
				ht := f.NewCASHitTracker(authenticatedContext(user1, authenticator), &repb.RequestMetadata{})
				ht.TrackDownload(aDigest).CloseWithBytesTransferred(1, 2, repb.Compressor_IDENTITY, "test")

				after, sample := i3WaitGoroutines("hit_tracker_client.(*HitTrackerFactory)", before)
				grpcAfter, _ := i3Goroutines("google.golang.org/grpc")
				t.Logf("n=%d failing=%v: hit_tracker goroutines before=%d running=%d afterShutdown=%d; grpc goroutines before=%d afterShutdown=%d; rpcs=%d hitsDelivered=%d",
					n, failing, before, running, after, grpcBefore, grpcAfter, svc.calls.Load(), svc.hits.Load())
				require.LessOrEqual(t, after, before, "leftover goroutine:\n%s", sample)
			})
		}
	}
}
