package content_addressable_storage_server_proxy

import (
	"context"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/atime_updater"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/authutil"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// When the backing cache evicts a blob after the proxy has cached a
// "present" FindMissingBlobs answer for it, the proxy keeps reporting the blob
// as present until the presence-cache TTL expires, without contacting the
// backend, even though neither the proxy nor the backend can serve it.
func TestFindMissingBlobs_CachedPresenceOutlivesBackendEviction(t *testing.T) {
	ttl := time.Hour
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", ttl)
	ctx := testContext()
	remoteEnv := testenv.GetTestEnv(t)
	conn, requestCount, _ := runRemoteCASS(ctx, remoteEnv, t)
	proxyEnv := testenv.GetTestEnv(t)
	clock := clockwork.NewFakeClock()
	proxyEnv.SetClock(clock)
	proxyEnv.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	proxyEnv.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))
	flags.Set(t, "cache_proxy.remote_atime_update_interval", atimeUpdatePeriod)
	require.NoError(t, atime_updater.Register(proxyEnv))
	proxyConn := runCASProxy(ctx, conn, proxyEnv, t)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)
	ctx = metadata.AppendToOutgoingContext(ctx, authutil.APIKeyHeader, "US1")

	// The blob exists only in the backing cache (e.g. written by another
	// client or by an executor that talks to the backend directly).
	barDigestProto := digestProto(barDigest, 3)
	remote := repb.NewContentAddressableStorageClient(conn)
	_, err := remote.BatchUpdateBlobs(ctx, updateBlobsRequest(map[*repb.Digest]string{barDigestProto: "bar"}))
	require.NoError(t, err)

	// First check goes to the backend and populates the presence cache.
	requestCount.Store(0)
	findMissing(ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(1), requestCount.Load())

	// Repeated presence checks over most of the TTL never reach the backend,
	// so the backend sees no access to the blob during this time.
	for range 50 {
		clock.Advance(ttl / 60)
		findMissing(ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	}
	require.Equal(t, int32(1), requestCount.Load(), "backend should not have been contacted by cached presence checks")

	// The backend evicts the blob (simulated by deleting it from the
	// backend's cache, using the same key the backend's CAS server uses).
	remoteCtx, err := prefix.AttachUserPrefixToContext(context.Background(), remoteEnv.GetAuthenticator())
	require.NoError(t, err)
	rn := digest.NewCASResourceName(barDigestProto, "", repb.DigestFunction_SHA256)
	require.NoError(t, remoteEnv.GetCache().Delete(remoteCtx, rn.ToProto()))
	rsp, err := remote.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{barDigestProto}))
	require.NoError(t, err)
	require.Len(t, rsp.GetMissingBlobDigests(), 1, "backend should now report the blob missing")

	// Neither the proxy nor the backend can serve the blob.
	readRsp, err := proxy.BatchReadBlobs(ctx, readBlobsRequest([]*repb.Digest{barDigestProto}))
	require.NoError(t, err)
	require.Len(t, readRsp.GetResponses(), 1)
	require.Equal(t, int32(codes.NotFound), readRsp.GetResponses()[0].GetStatus().GetCode())

	// Expected: the proxy reports the blob missing so that clients re-upload
	// it. Observed at baseline: the proxy still reports it present.
	proxyRsp, err := proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{barDigestProto}))
	require.NoError(t, err)
	require.Len(t, proxyRsp.GetMissingBlobDigests(), 1,
		"proxy reports a blob present that neither it nor the backend has (presence cache TTL %s)", ttl)
}
