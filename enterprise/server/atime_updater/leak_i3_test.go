package atime_updater

import (
	"context"
	"fmt"
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

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

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

type i3FailingCAS struct {
	*fakeCAS
	fail  bool
	calls atomic.Int64
}

func (f *i3FailingCAS) FindMissingBlobs(ctx context.Context, req *repb.FindMissingBlobsRequest) (*repb.FindMissingBlobsResponse, error) {
	f.calls.Add(1)
	if f.fail {
		return nil, status.UnavailableError("remote failing")
	}
	return f.fakeCAS.FindMissingBlobs(ctx, req)
}

// Drives atime updates through the real Register path and shuts down through
// the health checker, then checks that the batch operator goroutines exit.
func TestI3_AtimeUpdaterShutdown(t *testing.T) {
	const marker = "batch_operator.(*batchOperator)"
	for _, failing := range []bool{false, true} {
		for _, n := range []int{10, 1000, 20000} {
			t.Run(fmt.Sprintf("failing=%v/n=%d", failing, n), func(t *testing.T) {
				flags.Set(t, "cache_proxy.remote_atime_update_interval", 100*time.Millisecond)
				flags.Set(t, "cache_proxy.remote_atime_queue_size", 100_000)
				before, _ := i3Goroutines(marker)
				env := testenv.GetTestEnv(t)
				authenticator := testauth.NewTestAuthenticator(t, testauth.TestUsers(user1, group1, user2, group2))
				env.SetAuthenticator(authenticator)
				cas := &i3FailingCAS{fakeCAS: &fakeCAS{t: t, authenticator: authenticator}, fail: failing}
				grpcServer, runFunc, lis := testenv.RegisterLocalGRPCServer(t, env)
				repb.RegisterContentAddressableStorageServer(grpcServer, cas)
				go runFunc()
				conn, err := testenv.LocalGRPCConn(t, context.Background(), lis)
				require.NoError(t, err)
				t.Cleanup(func() { conn.Close() })
				env.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))

				// Same as Register, minus registering with the cache (which
				// gives no handle back to drive Enqueue directly).
				updater, err := new(env)
				require.NoError(t, err)
				updater.Start(env.GetHealthChecker())
				running, _ := i3Goroutines(marker)
				for i := range n {
					user := user1
					if i%2 == 1 {
						user = user2
					}
					ctx, cancel := context.WithCancel(authenticatedContext(context.Background(), user, authenticator))
					if i%3 == 0 {
						cancel() // request already cancelled
					}
					d := digestProto(fmt.Sprintf("%064d", i), 3)
					updater.Enqueue(ctx, fmt.Sprintf("inst-%d", i%5), []*repb.Digest{d}, repb.DigestFunction_SHA256)
					cancel()
				}
				time.Sleep(300 * time.Millisecond)
				env.GetHealthChecker().Shutdown()
				env.GetHealthChecker().WaitForGracefulShutdown()
				// Enqueue after shutdown must not block or spawn anything.
				updater.Enqueue(authenticatedContext(context.Background(), user1, authenticator), "", []*repb.Digest{digest1}, repb.DigestFunction_SHA256)
				after, sample := i3WaitGoroutines(marker, before)
				t.Logf("n=%d failing=%v: batch_operator goroutines before=%d running=%d afterShutdown=%d; FindMissingBlobs calls=%d delivered=%d",
					n, failing, before, running, after, cas.calls.Load(), len(cas.getUpdates()))
				require.LessOrEqual(t, after, before, "leftover goroutine:\n%s", sample)
			})
		}
	}
}
