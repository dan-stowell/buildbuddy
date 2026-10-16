package content_addressable_storage_server_proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/atime_updater"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/authutil"
	"github.com/buildbuddy-io/buildbuddy/server/util/cdc"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/buildbuddy-io/buildbuddy/server/util/proto"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc/status"
)

// When the backing cache evicts a blob after the proxy has cached a
// "present" FindMissingBlobs answer for it, the proxy keeps reporting the blob
// as present until the presence-cache TTL expires, without contacting the
// backend, even though neither the proxy nor the backend can serve it.
func TestFindMissingBlobs_CachedPresenceOutlivesBackendEviction(t *testing.T) {
	for _, method := range []string{"batch", "bytestream", "compressed_bytestream", "tree", "cached_tree"} {
		t.Run(method, func(t *testing.T) { testPresenceAfterBackendEviction(t, method) })
	}
}

func testPresenceAfterBackendEviction(t *testing.T, method string) {
	flags.Set(t, "cache_proxy.enable_get_tree_caching", method == "cached_tree")
	ttl := time.Hour
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", ttl)
	flags.Set(t, "cache_proxy.find_missing_blobs_refresh_interval", ttl)
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

	// Warm the chunk presence cache too. A failed read must invalidate both.
	chunkCtx := metadata.AppendToOutgoingContext(ctx, cdc.ChunkedHeaderName, "true")
	findMissing(ctx, proxy, []*repb.Digest{barDigestProto}, nil, t)
	findMissing(chunkCtx, proxy, []*repb.Digest{barDigestProto}, nil, t)

	// The backend evicts the blob (simulated by deleting it from the
	// backend's cache, using the same key the backend's CAS server uses).
	remoteCtx, err := prefix.AttachUserPrefixToContext(context.Background(), remoteEnv.GetAuthenticator())
	require.NoError(t, err)
	rn := digest.NewCASResourceName(barDigestProto, "", repb.DigestFunction_SHA256)
	require.NoError(t, remoteEnv.GetCache().Delete(remoteCtx, rn.ToProto()))
	rsp, err := remote.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{barDigestProto}))
	require.NoError(t, err)
	require.Len(t, rsp.GetMissingBlobDigests(), 1, "backend should now report the blob missing")

	switch method {
	case "batch":
		readRsp, err := proxy.BatchReadBlobs(ctx, readBlobsRequest([]*repb.Digest{barDigestProto}))
		require.NoError(t, err)
		require.Len(t, readRsp.GetResponses(), 1)
		require.Equal(t, int32(codes.NotFound), readRsp.GetResponses()[0].GetStatus().GetCode())
	case "bytestream", "compressed_bytestream":
		if method == "compressed_bytestream" {
			rn.SetCompressor(repb.Compressor_ZSTD)
		}
		stream, err := bspb.NewByteStreamClient(proxyConn).Read(ctx, &bspb.ReadRequest{ResourceName: rn.DownloadString()})
		require.NoError(t, err)
		_, err = stream.Recv()
		require.Equal(t, codes.NotFound, status.Code(err))
	case "tree", "cached_tree":
		stream, err := proxy.GetTree(ctx, &repb.GetTreeRequest{RootDigest: barDigestProto, DigestFunction: repb.DigestFunction_SHA256})
		require.NoError(t, err)
		_, err = stream.Recv()
		require.Equal(t, codes.NotFound, status.Code(err))
	}

	// Expected: the proxy reports the blob missing so that clients re-upload
	// it. Observed at baseline: the proxy still reports it present.
	proxyRsp, err := proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{barDigestProto}))
	require.NoError(t, err)
	require.Len(t, proxyRsp.GetMissingBlobDigests(), 1,
		"proxy reports a blob present that neither it nor the backend has (presence cache TTL %s)", ttl)
	findMissing(chunkCtx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{barDigestProto}, t)
}

// Hot presence entries must periodically contact the backend, while repeated
// checks inside the refresh interval remain local. Expiration does not slide.
func TestFindMissingBlobs_BoundedPresenceRefresh(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprint(chunked), func(t *testing.T) {
			flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Hour)
			flags.Set(t, "cache_proxy.find_missing_blobs_refresh_interval", time.Minute)
			ctx := testContext()
			remoteEnv := testenv.GetTestEnv(t)
			conn, count, _ := runRemoteCASS(ctx, remoteEnv, t)
			env := testenv.GetTestEnv(t)
			clock := clockwork.NewFakeClock()
			env.SetClock(clock)
			env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
			client := repb.NewContentAddressableStorageClient(runCASProxy(ctx, conn, env, t))
			ctx = metadata.AppendToOutgoingContext(ctx, authutil.APIKeyHeader, "US1")
			if chunked {
				ctx = metadata.AppendToOutgoingContext(ctx, cdc.ChunkedHeaderName, "true")
			}
			ds := []*repb.Digest{digestProto(fooDigest, 3), digestProto(barDigest, 3)}
			update(ctx, repb.NewContentAddressableStorageClient(conn), map[*repb.Digest]string{ds[0]: "foo", ds[1]: "bar"}, t)
			count.Store(0)
			findMissing(ctx, client, ds, nil, t)
			for range 59 {
				clock.Advance(time.Second)
				findMissing(ctx, client, ds, nil, t)
			}
			require.Equal(t, int32(1), count.Load())
			clock.Advance(time.Second)
			findMissing(ctx, client, ds, nil, t)
			require.Equal(t, int32(2), count.Load(), "refresh all expired digests in one backend request")
		})
	}
}

// A remote GetTree failure may identify no digest. Invalidate descendants too.
func TestGetTree_MissingDescendantInvalidatesPresence(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Hour)
	ctx := testContext()
	remoteEnv := testenv.GetTestEnv(t)
	conn, _, _ := runRemoteCASS(ctx, remoteEnv, t)
	env := testenv.GetTestEnv(t)
	env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	client := repb.NewContentAddressableStorageClient(runCASProxy(ctx, conn, env, t))
	ctx = metadata.AppendToOutgoingContext(ctx, authutil.APIKeyHeader, "US1")
	childData, err := proto.Marshal(&repb.Directory{Files: []*repb.FileNode{{Name: "file", Digest: digestProto(fooDigest, 3)}}})
	require.NoError(t, err)
	child, err := digest.Compute(bytes.NewReader(childData), repb.DigestFunction_SHA256)
	require.NoError(t, err)
	rootData, err := proto.Marshal(&repb.Directory{Directories: []*repb.DirectoryNode{{Name: "child", Digest: child}}})
	require.NoError(t, err)
	root, err := digest.Compute(bytes.NewReader(rootData), repb.DigestFunction_SHA256)
	require.NoError(t, err)
	remote := repb.NewContentAddressableStorageClient(conn)
	update(ctx, remote, map[*repb.Digest]string{root: string(rootData), child: string(childData)}, t)
	findMissing(ctx, client, []*repb.Digest{root, child}, nil, t)
	remoteCtx, err := prefix.AttachUserPrefixToContext(context.Background(), remoteEnv.GetAuthenticator())
	require.NoError(t, err)
	require.NoError(t, remoteEnv.GetCache().Delete(remoteCtx, digest.NewCASResourceName(child, "", repb.DigestFunction_SHA256).ToProto()))
	stream, err := client.GetTree(ctx, &repb.GetTreeRequest{RootDigest: root, DigestFunction: repb.DigestFunction_SHA256})
	require.NoError(t, err)
	for {
		_, err = stream.Recv()
		if err != nil {
			break
		}
	}
	require.NotEqual(t, io.EOF, err)
	require.Equal(t, codes.NotFound, status.Code(err))
	findMissing(ctx, client, []*repb.Digest{root, child}, []*repb.Digest{child}, t)
}
