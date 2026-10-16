package build_event_handler_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/backends/memory_metrics_collector"
	"github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_handler"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/authutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"

	bspb "github.com/buildbuddy-io/buildbuddy/proto/build_event_stream"
	inpb "github.com/buildbuddy-io/buildbuddy/proto/invocation"
	inspb "github.com/buildbuddy-io/buildbuddy/proto/invocation_status"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// recordACHits simulates the Bazel client hitting the remote action cache
// n times. Cache traffic is tagged with the Bazel invocation ID (which stays
// the same across BES stream retries, since it is the same Bazel command).
func recordACHits(t *testing.T, te *testenv.TestEnv, ctx context.Context, iid string, start, n int) {
	for i := start; i < start+n; i++ {
		rmd := &repb.RequestMetadata{
			ToolInvocationId: iid,
			ActionId:         fmt.Sprintf("action-%d", i),
			ActionMnemonic:   "GoCompile",
			TargetId:         "//foo:bar",
		}
		ht := te.GetHitTrackerFactory().NewACHitTracker(ctx, rmd)
		d := &repb.Digest{Hash: fmt.Sprintf("%064d", i), SizeBytes: 100}
		require.NoError(t, ht.TrackDownload(d).CloseWithBytesTransferred(100, 100, repb.Compressor_IDENTITY, "test"))
	}
}

func lookupACHits(t *testing.T, te *testenv.TestEnv, ctx context.Context, iid string) (*inpb.Invocation, int64) {
	inv, err := build_event_handler.LookupInvocation(te, ctx, iid)
	require.NoError(t, err)
	return inv, inv.GetCacheStats().GetActionCacheHits()
}

// TestCacheStatsAfterBESRetryOnDisconnect models a Bazel build whose BES
// stream is cut mid-build (e.g. app rolling deploy / LB reset). Bazel keeps
// building (and keeps using the remote cache under the same invocation ID),
// then retries the BES upload on a new stream starting at sequence number 1
// (BuildBuddy never ACKs before the stream half-closes, so nothing is acked).
//
// The Bazel command performs 3 AC hits before the first disconnect, 1 more
// between each subsequent disconnect, and 2 after the last retry. The final
// (COMPLETE) attempt should report all of them, as it would without a retry.
func TestCacheStatsAfterBESRetryOnDisconnect(t *testing.T) {
	for _, disconnects := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("disconnects=%d", disconnects), func(t *testing.T) {
			te := testenv.GetTestEnv(t)
			auth := testauth.NewTestAuthenticator(t, testauth.TestUsers("USER1", "GROUP1"))
			te.SetAuthenticator(auth)
			mc, err := memory_metrics_collector.NewMemoryMetricsCollector()
			require.NoError(t, err)
			te.SetMetricsCollector(mc)
			ctx := context.Background()
			userCtx := auth.AuthContextFromAPIKey(ctx, "USER1")
			iid := uuid.New().String()
			handler := build_event_handler.NewBuildEventHandler(te)

			started := func() *anypb.Any {
				return startedEvent("--remote_header='"+authutil.APIKeyHeader+"=USER1'", &bspb.BuildEventId_WorkspaceStatus{})
			}

			nextAction := 0
			hit := func(n int) {
				recordACHits(t, te, userCtx, iid, nextAction, n)
				nextAction += n
			}

			// Bazel builds; 3 AC hits before the first disconnect.
			hit(3)
			for attempt := 1; attempt <= disconnects; attempt++ {
				ch, err := handler.OpenChannel(ctx, iid)
				require.NoError(t, err)
				require.NoError(t, ch.HandleEvent(streamRequest(started(), iid, 1)))
				require.NoError(t, ch.HandleEvent(streamRequest(workspaceStatusEvent("COMMIT_SHA", "abc123"), iid, 2)))
				if attempt > 1 {
					hit(1)
				}
				// Stream is cut before BuildFinished: the server calls
				// FinalizeInvocation from disconnectWithErr.
				require.NoError(t, ch.FinalizeInvocation(iid))
				ch.Close()
				inv, _ := lookupACHits(t, te, userCtx, iid)
				require.Equal(t, inspb.InvocationStatus_DISCONNECTED_INVOCATION_STATUS, inv.GetInvocationStatus())
				require.Equal(t, uint64(attempt), inv.GetAttempt())

				// Bazel's BES uploader backs off (~1s) before retrying,
				// which is longer than the stats finalization delay.
				time.Sleep(1500 * time.Millisecond)
			}

			// Final attempt: new stream (possibly on another replica),
			// resending everything from sequence number 1.
			ch, err := handler.OpenChannel(ctx, iid)
			require.NoError(t, err)
			require.NoError(t, ch.HandleEvent(streamRequest(started(), iid, 1)))
			require.NoError(t, ch.HandleEvent(streamRequest(workspaceStatusEvent("COMMIT_SHA", "abc123"), iid, 2)))
			// Bazel keeps building; 2 more AC hits.
			hit(2)
			require.NoError(t, ch.HandleEvent(streamRequest(finishedEvent(), iid, 3)))
			require.NoError(t, ch.FinalizeInvocation(iid))
			ch.Close()
			inv, _ := lookupACHits(t, te, userCtx, iid)
			require.Equal(t, uint64(disconnects+1), inv.GetAttempt())
			require.Equal(t, inspb.InvocationStatus_COMPLETE_INVOCATION_STATUS, inv.GetInvocationStatus())

			// Wait for the stats recorder (500ms finalization delay) to
			// write the final attempt's cache stats into the invocation row.
			var hits int64
			require.Eventually(t, func() bool {
				inv, hits = lookupACHits(t, te, userCtx, iid)
				return hits != 0
			}, 10*time.Second, 50*time.Millisecond)
			time.Sleep(1 * time.Second) // let any further stats writes settle
			inv, hits = lookupACHits(t, te, userCtx, iid)
			require.Equal(t, inspb.InvocationStatus_COMPLETE_INVOCATION_STATUS, inv.GetInvocationStatus())
			require.Equal(t, uint64(disconnects+1), inv.GetAttempt())
			require.Equal(t, int64(nextAction), hits, "final AC hit count for invocation (attempt %d)", inv.GetAttempt())
		})
	}
}
