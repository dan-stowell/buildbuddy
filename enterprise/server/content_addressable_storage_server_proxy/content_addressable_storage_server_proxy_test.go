package content_addressable_storage_server_proxy

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/atime_updater"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/byte_stream_server_proxy"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/experiments"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/proxy_util"
	"github.com/buildbuddy-io/buildbuddy/server/backends/memory_cache"
	"github.com/buildbuddy-io/buildbuddy/server/metrics"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/byte_stream_server"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/cachetools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/content_addressable_storage_server"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/cas"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/authutil"
	"github.com/buildbuddy-io/buildbuddy/server/util/cdc"
	"github.com/buildbuddy-io/buildbuddy/server/util/findmissing"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/buildbuddy-io/buildbuddy/server/util/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/open-feature/go-sdk/openfeature"
	"github.com/open-feature/go-sdk/openfeature/memprovider"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	bspb "google.golang.org/genproto/googleapis/bytestream"
)

const (
	fooDigest   = "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"
	foofDigest  = "ecebed81d223de4ccfbcf9cee4e19e1872165b8a142c2d6ee6fb1d29617d0e8e"
	barDigest   = "fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9"
	barrDigest  = "8fa319f9b487d6ae32862c952d708b192a999b2f96bda081e8a49a0c3fb99265"
	barrrDigest = "39938f9489bc9b0f9d7308be111b90a615942ebc4530f0bf5c98e6083af29ee8"
	bazDigest   = "baa5a0964d3320fbc0c6a922140453c8513ea24ab8fd0577034804a967248096"
	quxDigest   = "21f58d27f827d295ffcd860c65045685e3baf1ad4506caa0140113b316647534"

	atimeUpdatePeriod = time.Minute
)

func requestCountingUnaryInterceptor(count *atomic.Int32) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		count.Add(1)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func requestCountingStreamInterceptor(count *atomic.Int32) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		count.Add(1)
		return streamer(ctx, desc, cc, method, opts...)
	}
}

func runRemoteCASS(ctx context.Context, env *testenv.TestEnv, t testing.TB) (*grpc.ClientConn, *atomic.Int32, *atomic.Int32) {
	casServer, err := content_addressable_storage_server.NewContentAddressableStorageServer(env)
	require.NoError(t, err)
	bsServer, err := byte_stream_server.NewByteStreamServer(env)
	require.NoError(t, err)
	grpcServer, runFunc, lis := testenv.RegisterLocalGRPCServer(t, env)
	repb.RegisterContentAddressableStorageServer(grpcServer, casServer)
	bspb.RegisterByteStreamServer(grpcServer, bsServer)
	go runFunc()
	unaryRequestCounter := atomic.Int32{}
	streamRequestCounter := atomic.Int32{}
	conn, err := testenv.LocalGRPCConn(t, ctx, lis,
		grpc.WithUnaryInterceptor(requestCountingUnaryInterceptor(&unaryRequestCounter)),
		grpc.WithStreamInterceptor(requestCountingStreamInterceptor(&streamRequestCounter)))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn, &unaryRequestCounter, &streamRequestCounter
}

func runLocalCASS(ctx context.Context, env *testenv.TestEnv, t testing.TB) repb.ContentAddressableStorageServer {
	cas, err := content_addressable_storage_server.NewContentAddressableStorageServer(env)
	require.NoError(t, err)
	return cas
}

func runCASProxy(ctx context.Context, clientConn *grpc.ClientConn, env *testenv.TestEnv, t testing.TB) *grpc.ClientConn {
	env.SetByteStreamClient(bspb.NewByteStreamClient(clientConn))
	env.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(clientConn))
	bss, err := byte_stream_server.NewByteStreamServer(env)
	require.NoError(t, err)
	env.SetLocalByteStreamServer(bss)
	cas, err := content_addressable_storage_server.NewContentAddressableStorageServer(env)
	require.NoError(t, err)
	env.SetLocalCASServer(cas)
	casServer, err := New(env)
	require.NoError(t, err)
	env.SetCASServer(casServer)
	bsServer, err := byte_stream_server_proxy.New(env)
	require.NoError(t, err)
	grpcServer, runFunc, lis := testenv.RegisterLocalGRPCServer(t, env)
	repb.RegisterContentAddressableStorageServer(grpcServer, casServer)
	bspb.RegisterByteStreamServer(grpcServer, bsServer)
	go runFunc()
	conn, err := testenv.LocalGRPCConn(t, ctx, lis, grpc.WithDefaultCallOptions())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}

func digestProto(hash string, size int64) *repb.Digest {
	return &repb.Digest{Hash: hash, SizeBytes: size}
}

func findMissingBlobsRequest(digests []*repb.Digest) *repb.FindMissingBlobsRequest {
	return &repb.FindMissingBlobsRequest{
		BlobDigests:    digests,
		DigestFunction: repb.DigestFunction_SHA256,
	}
}

func readBlobsRequest(digests []*repb.Digest) *repb.BatchReadBlobsRequest {
	return &repb.BatchReadBlobsRequest{
		Digests:               digests,
		AcceptableCompressors: []repb.Compressor_Value{repb.Compressor_IDENTITY},
		DigestFunction:        repb.DigestFunction_SHA256,
	}
}

func updateBlobsRequest(blobs map[*repb.Digest]string) *repb.BatchUpdateBlobsRequest {
	request := repb.BatchUpdateBlobsRequest{DigestFunction: repb.DigestFunction_SHA256}
	for digest, data := range blobs {
		request.Requests = append(request.Requests, &repb.BatchUpdateBlobsRequest_Request{
			Digest: digest,
			Data:   []byte(data),
		})
	}
	return &request
}

func findMissing(ctx context.Context, client repb.ContentAddressableStorageClient, digests []*repb.Digest, missing []*repb.Digest, t *testing.T) {
	resp, err := client.FindMissingBlobs(ctx, findMissingBlobsRequest(digests))
	require.NoError(t, err)
	require.Equal(t, len(missing), len(resp.MissingBlobDigests))
	for i := range missing {
		require.Equal(t, missing[i].Hash, resp.MissingBlobDigests[i].Hash)
		require.Equal(t, missing[i].SizeBytes, resp.MissingBlobDigests[0].SizeBytes)
	}
}

func read(ctx context.Context, client repb.ContentAddressableStorageClient, digests []*repb.Digest, blobs map[string]string, t *testing.T) {
	resp, err := client.BatchReadBlobs(ctx, readBlobsRequest(digests))
	require.NoError(t, err)
	require.Equal(t, len(digests), len(resp.Responses))
	expectedCount := map[string]int{}
	for _, digest := range digests {
		if _, ok := expectedCount[digest.Hash]; ok {
			expectedCount[digest.Hash] = expectedCount[digest.Hash] + 1
		} else {
			expectedCount[digest.Hash] = 1
		}
	}
	actualCount := map[string]int{}
	for _, response := range resp.Responses {
		hash := response.Digest.Hash
		if _, ok := actualCount[hash]; ok {
			actualCount[hash] = actualCount[hash] + 1
		} else {
			actualCount[hash] = 1
		}
		if _, ok := blobs[hash]; ok {
			require.Equal(t, int32(codes.OK), response.Status.Code)
			require.Equal(t, blobs[hash], string(response.Data))
		} else {
			require.Equal(t, int32(codes.NotFound), response.Status.Code)
		}
	}
	require.Equal(t, expectedCount, actualCount)
}

func update(ctx context.Context, client repb.ContentAddressableStorageClient, blobs map[*repb.Digest]string, t testing.TB) {
	resp, err := client.BatchUpdateBlobs(ctx, updateBlobsRequest(blobs))
	require.NoError(t, err)
	require.Equal(t, len(blobs), len(resp.Responses))
	for i := 0; i < len(blobs); i++ {
		require.Equal(t, int32(codes.OK), resp.Responses[i].Status.Code)
	}
}

func TestBatchUpdateBlobsCompressorMetricsLabelsAreBounded(t *testing.T) {
	cm := newCacheMetrics().addUpdateMetrics([]*repb.BatchUpdateBlobsRequest_Request{
		{Compressor: repb.Compressor_IDENTITY, Data: []byte("a")},
		{Compressor: repb.Compressor_ZSTD, Data: []byte("bb")},
		{Compressor: repb.Compressor_Value(123), Data: []byte("ccc")},
		{Compressor: repb.Compressor_Value(456), Data: []byte("dddd")},
	})

	gotDigests := map[string]int{}
	gotBytes := map[string]int{}
	for _, byCompressor := range cm.digestsPerStatusAndCompressor {
		maps.Copy(gotDigests, byCompressor)
	}
	for _, byCompressor := range cm.bytesPerStatusAndCompressor {
		maps.Copy(gotBytes, byCompressor)
	}
	require.Equal(t, map[string]int{"IDENTITY": 1, "ZSTD": 1, "unknown": 2}, gotDigests)
	require.Equal(t, map[string]int{"IDENTITY": 1, "ZSTD": 2, "unknown": 7}, gotBytes)
}

func expectAtimeUpdate(t *testing.T, clock *clockwork.FakeClock, requestCount *atomic.Int32) {
	requestCount.Store(0)
	wait := time.Millisecond
	for range 10 {
		// The read that enqueued this atime update is processed asynchronously
		// by the batcher goroutine, so the pending batch may not be ready when
		// the sender's flush fires. Advance the clock on every iteration (rather
		// than only once up front) so that a later tick still flushes the update
		// once the batcher has caught up.
		clock.Advance(atimeUpdatePeriod + time.Second)
		time.Sleep(wait)
		wait = wait * 2
		// A single read can enqueue multiple digests (e.g. read(foo, baz)
		// updates the atime of both). If the batcher splits them across
		// pending batches, they may flush on separate ticks, so more than one
		// update RPC can be observed here. Accept >= 1 rather than == 1 so we
		// don't spuriously fail when a second flush has already landed.
		if requestCount.Load() >= 1 {
			requestCount.Store(0)
			return
		}
	}
	t.Fatal("Timed out waiting for remote atime update")
}

func expectNoAtimeUpdate(t *testing.T, clock *clockwork.FakeClock, requestCount *atomic.Int32) {
	requestCount.Store(0)
	for range 10 {
		clock.Advance(atimeUpdatePeriod + time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, int32(0), requestCount.Load())
}

func testContext() context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(authutil.ClientIdentityHeaderName, "fakeheader"))
}

func TestFindMissingBlobs(t *testing.T) {
	ctx := testContext()
	conn, requestCount, _ := runRemoteCASS(ctx, testenv.GetTestEnv(t), t)
	proxyEnv := testenv.GetTestEnv(t)
	clock := clockwork.NewFakeClock()
	proxyEnv.SetClock(clock)
	proxyEnv.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))
	flags.Set(t, "cache_proxy.remote_atime_update_interval", atimeUpdatePeriod)
	require.NoError(t, atime_updater.Register(proxyEnv))
	proxyConn := runCASProxy(ctx, conn, proxyEnv, t)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)

	fooDigestProto := digestProto(fooDigest, 3)
	barDigestProto := digestProto(barDigest, 3)

	for i := 1; i < 10; i++ {
		findMissing(ctx, proxy, []*repb.Digest{fooDigestProto}, []*repb.Digest{fooDigestProto}, t)
		require.Equal(t, int32(i), requestCount.Load())
	}
	expectNoAtimeUpdate(t, clock, requestCount)

	update(ctx, proxy, map[*repb.Digest]string{barDigestProto: "bar"}, t)

	requestCount.Store(0)
	for i := 1; i < 10; i++ {
		findMissing(ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
		require.Equal(t, int32(i), requestCount.Load())
	}
	expectNoAtimeUpdate(t, clock, requestCount)

	requestCount.Store(0)
	for i := 1; i < 10; i++ {
		findMissing(ctx, proxy, []*repb.Digest{fooDigestProto, barDigestProto}, []*repb.Digest{fooDigestProto}, t)
		require.Equal(t, int32(i), requestCount.Load())
	}
	expectNoAtimeUpdate(t, clock, requestCount)
}

func TestFindMissingBlobs_SkipRemote(t *testing.T) {
	ctx := testContext()
	remoteConn, _, _ := runRemoteCASS(ctx, testenv.GetTestEnv(t), t)
	proxyEnv := testenv.GetTestEnv(t)
	proxyEnv.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(remoteConn))
	require.NoError(t, atime_updater.Register(proxyEnv))
	proxyConn := runCASProxy(ctx, remoteConn, proxyEnv, t)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)

	digestA := digestProto(fooDigest, 3)
	digestB := digestProto(foofDigest, 4)
	digestC := digestProto(barDigest, 3)

	// Write digests A and B to the remote cache
	remote := proxyEnv.GetContentAddressableStorageClient()
	_, err := remote.BatchUpdateBlobs(ctx, updateBlobsRequest(map[*repb.Digest]string{digestA: "foo", digestB: "foof"}))
	require.NoError(t, err)

	// Write digests B and C to the local cache
	local := proxyEnv.GetLocalCASServer()
	_, err = local.BatchUpdateBlobs(ctx, updateBlobsRequest(map[*repb.Digest]string{digestB: "foof", digestC: "bar"}))
	require.NoError(t, err)

	// If hitting the remote cache, FindMissing(A, C) should return digest C
	req := findMissingBlobsRequest([]*repb.Digest{digestA, digestC})
	rsp, err := proxy.FindMissingBlobs(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 1, len(rsp.MissingBlobDigests))
	require.Equal(t, digestC.GetHash(), rsp.MissingBlobDigests[0].Hash)

	// If only hitting the local proxy cache, FindMissing(A, C) should return digest A
	skipRemoteCtx := metadata.AppendToOutgoingContext(ctx, proxy_util.SkipRemoteKey, "true")
	rsp, err = proxy.FindMissingBlobs(skipRemoteCtx, req)
	require.NoError(t, err)
	require.Equal(t, 1, len(rsp.MissingBlobDigests))
	require.Equal(t, digestA.GetHash(), rsp.MissingBlobDigests[0].Hash)
}

func TestFindMissingBlobs_Caching(t *testing.T) {
	ttl := 30 * time.Second
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", ttl)
	ctx := testContext()
	conn, requestCount, _ := runRemoteCASS(ctx, testenv.GetTestEnv(t), t)
	proxyEnv := testenv.GetTestEnv(t)
	clock := clockwork.NewFakeClock()
	proxyEnv.SetClock(clock)
	proxyEnv.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	proxyEnv.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))
	flags.Set(t, "cache_proxy.remote_atime_update_interval", atimeUpdatePeriod)
	require.NoError(t, atime_updater.Register(proxyEnv))
	proxyConn := runCASProxy(ctx, conn, proxyEnv, t)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)

	// The FindMissingBlobs cache is only used for authenticated requests.
	ctx = metadata.AppendToOutgoingContext(ctx, authutil.APIKeyHeader, "US1")

	fooDigestProto := digestProto(fooDigest, 3)
	barDigestProto := digestProto(barDigest, 3)

	update(ctx, proxy, map[*repb.Digest]string{barDigestProto: "bar"}, t)
	requestCount.Store(0)

	// The first check for a present blob goes to the remote; repeat checks
	// within the TTL are served from the local cache.
	findMissing(ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(1), requestCount.Load())
	for range 5 {
		findMissing(ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	}
	require.Equal(t, int32(1), requestCount.Load())

	// Missing blobs are never cached.
	for i := 1; i <= 3; i++ {
		findMissing(ctx, proxy, []*repb.Digest{fooDigestProto}, []*repb.Digest{fooDigestProto}, t)
		require.Equal(t, int32(1+i), requestCount.Load())
	}

	// Mixed requests are partially served from the cache.
	findMissing(ctx, proxy, []*repb.Digest{fooDigestProto, barDigestProto}, []*repb.Digest{fooDigestProto}, t)
	require.Equal(t, int32(5), requestCount.Load())

	// Once the TTL expires, cached blobs are re-checked against the remote.
	clock.Advance(ttl + time.Second)
	findMissing(ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(6), requestCount.Load())
	findMissing(ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(6), requestCount.Load())

	// Empty requests are served without a remote call.
	findMissing(ctx, proxy, []*repb.Digest{}, []*repb.Digest{}, t)
	require.Equal(t, int32(6), requestCount.Load())
}

func TestFindMissingBlobs_CachingSeparatesChunksAndBlobs(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", 30*time.Second)
	ctx := testContext()
	conn, requestCount, _ := runRemoteCASS(ctx, testenv.GetTestEnv(t), t)
	proxyEnv := testenv.GetTestEnv(t)
	proxyEnv.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	proxyEnv.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))
	require.NoError(t, atime_updater.Register(proxyEnv))
	proxyConn := runCASProxy(ctx, conn, proxyEnv, t)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)

	fooDigestProto := digestProto(fooDigest, 3)
	barDigestProto := digestProto(barDigest, 3)
	remote := repb.NewContentAddressableStorageClient(conn)
	update(ctx, remote, map[*repb.Digest]string{fooDigestProto: "foo", barDigestProto: "bar"}, t)
	requestCount.Store(0)

	blobCtx := metadata.AppendToOutgoingContext(ctx, authutil.APIKeyHeader, "US1")
	chunkCtx := cdc.ContextWithChunked(blobCtx)

	findMissing(blobCtx, proxy, []*repb.Digest{fooDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(1), requestCount.Load())
	findMissing(blobCtx, proxy, []*repb.Digest{fooDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(1), requestCount.Load())

	// A whole-blob cache entry cannot satisfy a chunk lookup.
	findMissing(chunkCtx, proxy, []*repb.Digest{fooDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(2), requestCount.Load())
	findMissing(chunkCtx, proxy, []*repb.Digest{fooDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(2), requestCount.Load())

	findMissing(chunkCtx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(3), requestCount.Load())

	// A whole-blob lookup falls back to the chunk cache.
	chunkFallbackCounter := findMissingBlobsCacheLookups(metrics.HitStatusLabel, false, "chunk_cache")
	chunkFallbackHitsBefore := testutil.ToFloat64(chunkFallbackCounter)
	findMissing(blobCtx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(3), requestCount.Load())
	require.Equal(t, 1.0, testutil.ToFloat64(chunkFallbackCounter)-chunkFallbackHitsBefore)
}

func TestFindMissingBlobs_BypassCache(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", 30*time.Second)
	testProvider := memprovider.NewInMemoryProvider(map[string]memprovider.InMemoryFlag{
		"cache_proxy.bypass_find_missing_cache": {
			State:          memprovider.Enabled,
			DefaultVariant: "true",
			Variants: map[string]any{
				"true": true,
			},
		},
	})
	require.NoError(t, openfeature.SetNamedProviderAndWait(t.Name(), testProvider))
	fp, err := experiments.NewFlagProvider(t.Name())
	require.NoError(t, err)
	ctx := testContext()
	conn, requestCount, _ := runRemoteCASS(ctx, testenv.GetTestEnv(t), t)
	proxyEnv := testenv.GetTestEnv(t)
	proxyEnv.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	proxyEnv.SetExperimentFlagProvider(fp)
	proxyEnv.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))
	require.NoError(t, atime_updater.Register(proxyEnv))
	proxyConn := runCASProxy(ctx, conn, proxyEnv, t)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)
	ctx = metadata.AppendToOutgoingContext(ctx, authutil.APIKeyHeader, "US1")

	barDigestProto := digestProto(barDigest, 3)
	update(ctx, proxy, map[*repb.Digest]string{barDigestProto: "bar"}, t)
	requestCount.Store(0)

	findMissing(ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	findMissing(ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(2), requestCount.Load())
}

func TestFindMissingBlobs_CachingIsolatedByGroup(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", 30*time.Second)
	ctx := testContext()
	conn, requestCount, _ := runRemoteCASS(ctx, testenv.GetTestEnv(t), t)
	proxyEnv := testenv.GetTestEnv(t)
	clock := clockwork.NewFakeClock()
	proxyEnv.SetClock(clock)
	proxyEnv.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1", "US2", "GR2")))
	proxyEnv.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))
	flags.Set(t, "cache_proxy.remote_atime_update_interval", atimeUpdatePeriod)
	require.NoError(t, atime_updater.Register(proxyEnv))
	proxyConn := runCASProxy(ctx, conn, proxyEnv, t)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)

	group1Ctx := metadata.AppendToOutgoingContext(ctx, authutil.APIKeyHeader, "US1")
	group2Ctx := metadata.AppendToOutgoingContext(ctx, authutil.APIKeyHeader, "US2")

	// Write the blob directly to the remote cache so FindMissingBlobs reports
	// it as present for every group.
	barDigestProto := digestProto(barDigest, 3)
	remote := repb.NewContentAddressableStorageClient(conn)
	_, err := remote.BatchUpdateBlobs(ctx, updateBlobsRequest(map[*repb.Digest]string{barDigestProto: "bar"}))
	require.NoError(t, err)
	requestCount.Store(0)

	// Group 1's first check goes to the remote and populates its cache entry.
	findMissing(group1Ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(1), requestCount.Load())
	findMissing(group1Ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(1), requestCount.Load())

	// Group 2's first check for the same digest must go to the remote:
	// group 1's cache entry must not be shared across groups.
	findMissing(group2Ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(2), requestCount.Load())
	findMissing(group2Ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(2), requestCount.Load())

	// ANON requests go in their own cache partition.
	findMissing(ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(3), requestCount.Load())
	findMissing(ctx, proxy, []*repb.Digest{barDigestProto}, []*repb.Digest{}, t)
	require.Equal(t, int32(3), requestCount.Load())
}

func TestReadUpdateBlobs(t *testing.T) {
	ctx := testContext()
	conn, requestCount, _ := runRemoteCASS(ctx, testenv.GetTestEnv(t), t)
	casClient := repb.NewContentAddressableStorageClient(conn)
	proxyEnv := testenv.GetTestEnv(t)
	clock := clockwork.NewFakeClock()
	proxyEnv.SetClock(clock)
	proxyEnv.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))
	flags.Set(t, "cache_proxy.remote_atime_update_interval", atimeUpdatePeriod)
	require.NoError(t, atime_updater.Register(proxyEnv))
	proxyConn := runCASProxy(ctx, conn, proxyEnv, t)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)

	fooDigestProto := digestProto(fooDigest, 3)
	foofDigestProto := digestProto(foofDigest, 4)
	barDigestProto := digestProto(barDigest, 3)
	barrDigestProto := digestProto(barrDigest, 4)
	barrrDigestProto := digestProto(barrrDigest, 5)
	bazDigestProto := digestProto(bazDigest, 3)
	quxDigestProto := digestProto(quxDigest, 3)

	read(ctx, proxy, []*repb.Digest{fooDigestProto, foofDigestProto, barDigestProto}, map[string]string{}, t)
	require.Equal(t, int32(1), requestCount.Load())
	expectNoAtimeUpdate(t, clock, requestCount)

	update(ctx, proxy, map[*repb.Digest]string{fooDigestProto: "foo"}, t)
	require.Equal(t, int32(1), requestCount.Load())
	expectNoAtimeUpdate(t, clock, requestCount)

	read(ctx, casClient, []*repb.Digest{fooDigestProto}, map[string]string{fooDigest: "foo"}, t)
	requestCount.Store(0)
	read(ctx, proxy, []*repb.Digest{fooDigestProto}, map[string]string{fooDigest: "foo"}, t)
	require.Equal(t, int32(0), requestCount.Load())
	expectAtimeUpdate(t, clock, requestCount)
	read(ctx, proxy, []*repb.Digest{fooDigestProto, fooDigestProto}, map[string]string{fooDigest: "foo"}, t)
	require.Equal(t, int32(0), requestCount.Load())
	expectAtimeUpdate(t, clock, requestCount)

	read(ctx, proxy, []*repb.Digest{barrDigestProto, barrrDigestProto, bazDigestProto}, map[string]string{}, t)
	require.Equal(t, int32(1), requestCount.Load())
	expectNoAtimeUpdate(t, clock, requestCount)
	update(ctx, casClient, map[*repb.Digest]string{bazDigestProto: "baz"}, t)
	require.Equal(t, int32(1), requestCount.Load())
	expectNoAtimeUpdate(t, clock, requestCount)

	read(ctx, proxy, []*repb.Digest{barrDigestProto, barrrDigestProto, bazDigestProto}, map[string]string{bazDigest: "baz"}, t)
	require.Equal(t, int32(1), requestCount.Load())
	expectNoAtimeUpdate(t, clock, requestCount)
	read(ctx, proxy, []*repb.Digest{fooDigestProto, bazDigestProto}, map[string]string{fooDigest: "foo", bazDigest: "baz"}, t)
	expectAtimeUpdate(t, clock, requestCount)

	update(ctx, casClient, map[*repb.Digest]string{quxDigestProto: "qux"}, t)
	read(ctx, proxy, []*repb.Digest{quxDigestProto, quxDigestProto}, map[string]string{quxDigest: "qux"}, t)
	expectNoAtimeUpdate(t, clock, requestCount)
}

func makeTree(ctx context.Context, client bspb.ByteStreamClient, t testing.TB) (*repb.Digest, []string) {
	child1 := uuid.New()
	digest1, files1 := cas.MakeTree(ctx, t, client, "", 2, 2)
	child2 := uuid.New()
	digest2, files2 := cas.MakeTree(ctx, t, client, "", 2, 2)

	// Upload a root directory containing both child directories.
	root := &repb.Directory{
		Directories: []*repb.DirectoryNode{
			&repb.DirectoryNode{
				Name:   child1,
				Digest: digest1,
			},
			&repb.DirectoryNode{
				Name:   child2,
				Digest: digest2,
			},
		},
	}
	rootDigest, err := cachetools.UploadProto(ctx, client, "", repb.DigestFunction_SHA256, root)
	require.NoError(t, err)
	children := append(files1, files2...)
	children = append(children, child1, child2)
	return rootDigest, children
}

func TestGetTree(t *testing.T) {
	testGetTree(t, true /* = withCaching */)
	testGetTree(t, false /* = withCaching */)
}

func testGetTree(t *testing.T, withCaching bool) {
	flags.Set(t, "cache_proxy.enable_get_tree_caching", withCaching)
	ctx := testContext()
	conn, unaryRequests, streamRequests := runRemoteCASS(ctx, testenv.GetTestEnv(t), t)
	casClient := repb.NewContentAddressableStorageClient(conn)
	bsClient := bspb.NewByteStreamClient(conn)
	proxyEnv := testenv.GetTestEnv(t)
	proxyConn := runCASProxy(ctx, conn, proxyEnv, t)
	casProxy := repb.NewContentAddressableStorageClient(proxyConn)
	bsProxy := bspb.NewByteStreamClient(proxyConn)

	// Full tree written to the remote.
	rootDigest, files := makeTree(ctx, bsClient, t)
	treeFiles := cas.ReadTree(ctx, t, casClient, "", rootDigest)
	require.ElementsMatch(t, files, treeFiles)
	unaryRequests.Store(0)
	streamRequests.Store(0)
	treeFiles = cas.ReadTree(ctx, t, casProxy, "", rootDigest)
	require.ElementsMatch(t, files, treeFiles)
	if withCaching {
		// The tree has 4 levels, so expect 4 unary requests.
		require.Equal(t, int32(4), unaryRequests.Load())
		require.Equal(t, int32(0), streamRequests.Load())
	} else {
		require.Equal(t, int32(0), unaryRequests.Load())
		require.Equal(t, int32(1), streamRequests.Load())
	}
	unaryRequests.Store(0)
	streamRequests.Store(0)
	treeFiles = cas.ReadTree(ctx, t, casProxy, "", rootDigest)
	require.ElementsMatch(t, files, treeFiles)
	require.Equal(t, int32(0), unaryRequests.Load())
	if withCaching {
		require.Equal(t, int32(0), streamRequests.Load())
	} else {
		require.Equal(t, int32(1), streamRequests.Load())
	}

	// Full tree written to the proxy.
	rootDigest, files = makeTree(ctx, bsProxy, t)
	treeFiles = cas.ReadTree(ctx, t, casClient, "", rootDigest)
	require.ElementsMatch(t, files, treeFiles)
	unaryRequests.Store(0)
	streamRequests.Store(0)
	treeFiles = cas.ReadTree(ctx, t, casProxy, "", rootDigest)
	require.ElementsMatch(t, files, treeFiles)
	require.Equal(t, int32(0), unaryRequests.Load())
	if withCaching {
		require.Equal(t, int32(0), streamRequests.Load())
	} else {
		require.Equal(t, int32(1), streamRequests.Load())
	}

	// Write two subtrees to the proxy and a root node to the remote.
	firstTreeRoot, firstTreeFiles := makeTree(ctx, bsProxy, t)
	secondTreeRoot, secondTreeFiles := makeTree(ctx, bsProxy, t)
	root := &repb.Directory{
		Directories: []*repb.DirectoryNode{
			&repb.DirectoryNode{
				Name:   "first",
				Digest: firstTreeRoot,
			},
			&repb.DirectoryNode{
				Name:   "second",
				Digest: secondTreeRoot,
			},
		},
	}
	rootDigest, err := cachetools.UploadProto(ctx, bsClient, "", repb.DigestFunction_SHA256, root)
	files = []string{"first", "second"}
	files = append(files, firstTreeFiles...)
	files = append(files, secondTreeFiles...)
	require.NoError(t, err)
	treeFiles = cas.ReadTree(ctx, t, casClient, "", rootDigest)
	require.ElementsMatch(t, files, treeFiles)
	unaryRequests.Store(0)
	streamRequests.Store(0)
	treeFiles = cas.ReadTree(ctx, t, casProxy, "", rootDigest)
	require.ElementsMatch(t, files, treeFiles)
	if withCaching {
		// Only the root note should be read from the remote.
		require.Equal(t, int32(1), unaryRequests.Load())
		require.Equal(t, int32(0), streamRequests.Load())
	} else {
		require.Equal(t, int32(0), unaryRequests.Load())
		require.Equal(t, int32(1), streamRequests.Load())
	}

	// Write two subtrees to the remote and a root node to the proxy.
	firstTreeRoot, firstTreeFiles = makeTree(ctx, bsClient, t)
	secondTreeRoot, secondTreeFiles = makeTree(ctx, bsClient, t)
	root = &repb.Directory{
		Directories: []*repb.DirectoryNode{
			&repb.DirectoryNode{
				Name:   "first",
				Digest: firstTreeRoot,
			},
			&repb.DirectoryNode{
				Name:   "second",
				Digest: secondTreeRoot,
			},
		},
	}
	rootDigest, err = cachetools.UploadProto(ctx, bsProxy, "", repb.DigestFunction_SHA256, root)
	files = []string{"first", "second"}
	files = append(files, firstTreeFiles...)
	files = append(files, secondTreeFiles...)
	require.NoError(t, err)
	treeFiles = cas.ReadTree(ctx, t, casClient, "", rootDigest)
	require.ElementsMatch(t, files, treeFiles)
	unaryRequests.Store(0)
	streamRequests.Store(0)
	treeFiles = cas.ReadTree(ctx, t, casProxy, "", rootDigest)
	require.ElementsMatch(t, files, treeFiles)
	if withCaching {
		// The subtrees but not root should be read from the remote.
		require.Equal(t, int32(4), unaryRequests.Load())
		require.Equal(t, int32(0), streamRequests.Load())
	} else {
		require.Equal(t, int32(0), unaryRequests.Load())
		require.Equal(t, int32(1), streamRequests.Load())
	}
}

func BenchmarkFindMissingBlobs(b *testing.B) {
	ctx := testContext()
	conn, _, _ := runRemoteCASS(ctx, testenv.GetTestEnv(b), b)
	proxyEnv := testenv.GetTestEnv(b)
	clock := clockwork.NewFakeClock()
	proxyEnv.SetClock(clock)
	proxyEnv.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))
	require.NoError(b, atime_updater.Register(proxyEnv))
	proxyConn := runCASProxy(ctx, conn, proxyEnv, b)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)

	fooDigestProto := digestProto(fooDigest, 3)
	foofDigestProto := digestProto(foofDigest, 4)
	barDigestProto := digestProto(barDigest, 3)
	barrDigestProto := digestProto(barrDigest, 4)
	barrrDigestProto := digestProto(barrrDigest, 5)
	bazDigestProto := digestProto(bazDigest, 3)
	quxDigestProto := digestProto(quxDigest, 3)
	update(ctx, proxy, map[*repb.Digest]string{
		fooDigestProto: "foo",
		barDigestProto: "bar",
		bazDigestProto: "baz",
		quxDigestProto: "qux",
	}, b)
	req := findMissingBlobsRequest([]*repb.Digest{
		fooDigestProto,
		foofDigestProto,
		barDigestProto,
		barrDigestProto,
		barrrDigestProto,
		bazDigestProto,
		quxDigestProto,
	})

	expected := []*repb.Digest{
		foofDigestProto,
		barrDigestProto,
		barrrDigestProto,
	}

	b.ReportAllocs()

	for b.Loop() {
		resp, err := proxy.FindMissingBlobs(ctx, req)
		require.NoError(b, err)
		require.ElementsMatch(b, expected, resp.GetMissingBlobDigests())
	}
}

func BenchmarkBatchReadBlobs(b *testing.B) {
	ctx := testContext()
	conn, _, _ := runRemoteCASS(ctx, testenv.GetTestEnv(b), b)
	proxyEnv := testenv.GetTestEnv(b)
	clock := clockwork.NewFakeClock()
	proxyEnv.SetClock(clock)
	proxyEnv.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))
	require.NoError(b, atime_updater.Register(proxyEnv))
	proxyConn := runCASProxy(ctx, conn, proxyEnv, b)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)

	fooDigestProto := digestProto(fooDigest, 3)
	foofDigestProto := digestProto(foofDigest, 4)
	barDigestProto := digestProto(barDigest, 3)
	barrDigestProto := digestProto(barrDigest, 4)
	barrrDigestProto := digestProto(barrrDigest, 5)
	bazDigestProto := digestProto(bazDigest, 3)
	quxDigestProto := digestProto(quxDigest, 3)
	blobs := map[*repb.Digest]string{
		fooDigestProto:   "foo",
		foofDigestProto:  "foof",
		barDigestProto:   "bar",
		barrDigestProto:  "barr",
		barrrDigestProto: "barrr",
		bazDigestProto:   "baz",
		quxDigestProto:   "qux",
	}

	update(ctx, proxy, blobs, b)
	req := readBlobsRequest(slices.Collect(maps.Keys(blobs)))

	b.ReportAllocs()

	for b.Loop() {
		resp, err := proxy.BatchReadBlobs(ctx, req)
		require.NoError(b, err)
		require.Equal(b, len(blobs), len(resp.GetResponses()))
	}
}

func BenchmarkBatchUpdateBlobs(b *testing.B) {
	ctx := testContext()
	conn, _, _ := runRemoteCASS(ctx, testenv.GetTestEnv(b), b)
	proxyEnv := testenv.GetTestEnv(b)
	clock := clockwork.NewFakeClock()
	proxyEnv.SetClock(clock)
	proxyEnv.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))
	require.NoError(b, atime_updater.Register(proxyEnv))
	proxyConn := runCASProxy(ctx, conn, proxyEnv, b)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)

	fooDigestProto := digestProto(fooDigest, 3)
	foofDigestProto := digestProto(foofDigest, 4)
	barDigestProto := digestProto(barDigest, 3)
	barrDigestProto := digestProto(barrDigest, 4)
	barrrDigestProto := digestProto(barrrDigest, 5)
	bazDigestProto := digestProto(bazDigest, 3)
	quxDigestProto := digestProto(quxDigest, 3)
	blobs := map[*repb.Digest]string{
		fooDigestProto:   "foo",
		foofDigestProto:  "foof",
		barDigestProto:   "bar",
		barrDigestProto:  "barr",
		barrrDigestProto: "barrr",
		bazDigestProto:   "baz",
		quxDigestProto:   "qux",
	}

	i := 0
	req := updateBlobsRequest(blobs)

	b.ReportAllocs()

	for b.Loop() {
		req.InstanceName = fmt.Sprintf("%d", i)
		resp, err := proxy.BatchUpdateBlobs(ctx, req)
		require.NoError(b, err)
		require.Equal(b, len(blobs), len(resp.Responses))
		for i := 0; i < len(blobs); i++ {
			require.Equal(b, int32(codes.OK), resp.Responses[i].Status.Code)
		}
		i++
	}
}

func TestSpliceBlob(t *testing.T) {
	ctx := testContext()
	remoteEnv := testenv.GetTestEnv(t)
	conn, requestCount, _ := runRemoteCASS(ctx, remoteEnv, t)
	proxyEnv := testenv.GetTestEnv(t)
	proxyConn := runCASProxy(ctx, conn, proxyEnv, t)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)

	chunk1 := []byte("chunk1data")
	chunk2 := []byte("chunk2data")
	chunk1Digest, err := digest.Compute(bytes.NewReader(chunk1), repb.DigestFunction_SHA256)
	require.NoError(t, err)
	chunk2Digest, err := digest.Compute(bytes.NewReader(chunk2), repb.DigestFunction_SHA256)
	require.NoError(t, err)

	update(ctx, proxy, map[*repb.Digest]string{
		chunk1Digest: string(chunk1),
		chunk2Digest: string(chunk2),
	}, t)

	blobData := append(chunk1, chunk2...)
	blobDigest, err := digest.Compute(bytes.NewReader(blobData), repb.DigestFunction_SHA256)
	require.NoError(t, err)

	requestCount.Store(0)
	spliceReq := &repb.SpliceBlobRequest{
		BlobDigest:     blobDigest,
		ChunkDigests:   []*repb.Digest{chunk1Digest, chunk2Digest},
		DigestFunction: repb.DigestFunction_SHA256,
	}
	spliceResp, err := proxy.SpliceBlob(ctx, spliceReq)
	require.NoError(t, err)
	require.Equal(t, blobDigest.Hash, spliceResp.BlobDigest.Hash)
	require.Equal(t, int32(1), requestCount.Load())
}

func TestSplitBlob(t *testing.T) {
	ctx := testContext()
	remoteEnv := testenv.GetTestEnv(t)
	conn, _, _ := runRemoteCASS(ctx, remoteEnv, t)
	proxyEnv := testenv.GetTestEnv(t)
	proxyConn := runCASProxy(ctx, conn, proxyEnv, t)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)
	remote := repb.NewContentAddressableStorageClient(conn)

	chunk1 := []byte("chunk1")
	chunk2 := []byte("chunk2")
	chunk1Digest, err := digest.Compute(bytes.NewReader(chunk1), repb.DigestFunction_SHA256)
	require.NoError(t, err)
	chunk2Digest, err := digest.Compute(bytes.NewReader(chunk2), repb.DigestFunction_SHA256)
	require.NoError(t, err)

	update(ctx, remote, map[*repb.Digest]string{
		chunk1Digest: string(chunk1),
		chunk2Digest: string(chunk2),
	}, t)

	blobData := append(chunk1, chunk2...)
	blobDigest, err := digest.Compute(bytes.NewReader(blobData), repb.DigestFunction_SHA256)
	require.NoError(t, err)

	_, err = remote.SpliceBlob(ctx, &repb.SpliceBlobRequest{
		BlobDigest:     blobDigest,
		ChunkDigests:   []*repb.Digest{chunk1Digest, chunk2Digest},
		DigestFunction: repb.DigestFunction_SHA256,
	})
	require.NoError(t, err)

	splitResp, err := proxy.SplitBlob(ctx, &repb.SplitBlobRequest{
		BlobDigest:     blobDigest,
		DigestFunction: repb.DigestFunction_SHA256,
	})
	require.NoError(t, err)
	require.Equal(t, 2, len(splitResp.ChunkDigests))
	require.Equal(t, chunk1Digest.Hash, splitResp.ChunkDigests[0].Hash)
	require.Equal(t, chunk2Digest.Hash, splitResp.ChunkDigests[1].Hash)
}

func TestChunkMappingRPCsForwarded(t *testing.T) {
	ctx := testContext()
	conn, _, streamRequests := runRemoteCASS(ctx, testenv.GetTestEnv(t), t)
	proxyConn := runCASProxy(ctx, conn, testenv.GetTestEnv(t), t)
	proxy := repb.NewContentAddressableStorageClient(proxyConn)

	streamRequests.Store(0)
	getStream, err := proxy.GetChunkMapping(ctx, &repb.GetChunkMappingRequest{})
	require.NoError(t, err)
	_, err = getStream.Recv()
	require.Equal(t, codes.Unimplemented, status.Code(err))
	require.Equal(t, int32(1), streamRequests.Load())

	registerStream, err := proxy.RegisterChunkMapping(ctx)
	require.NoError(t, err)
	_ = registerStream.Send(&repb.RegisterChunkMappingRequest{})
	_, err = registerStream.CloseAndRecv()
	require.Equal(t, codes.Unimplemented, status.Code(err))
	require.Equal(t, int32(2), streamRequests.Load())
}

func BenchmarkGetTree(b *testing.B) {
	flags.Set(b, "cache_proxy.enable_get_tree_caching", true)

	ctx := testContext()
	conn, unaryRequests, streamRequests := runRemoteCASS(ctx, testenv.GetTestEnv(b), b)
	proxyEnv := testenv.GetTestEnv(b)
	proxyConn := runCASProxy(ctx, conn, proxyEnv, b)
	casProxy := repb.NewContentAddressableStorageClient(proxyConn)
	bsProxy := bspb.NewByteStreamClient(proxyConn)

	// Generate a static tree to reduce benchmark variability.
	fileDigests := map[string]*repb.Digest{}
	data := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	for _, datum := range data {
		d, err := cachetools.UploadBlob(ctx, bsProxy, "", repb.DigestFunction_SHA256, bytes.NewReader([]byte(strings.Repeat(datum, 100))))
		require.NoError(b, err)
		fileDigests[datum] = d
	}

	subdirAB := &repb.Directory{
		Files: []*repb.FileNode{
			&repb.FileNode{Name: "a", Digest: fileDigests["a"]},
			&repb.FileNode{Name: "b", Digest: fileDigests["b"]},
		},
	}
	subdirCD := &repb.Directory{
		Files: []*repb.FileNode{
			&repb.FileNode{Name: "c", Digest: fileDigests["c"]},
			&repb.FileNode{Name: "d", Digest: fileDigests["d"]},
		},
	}
	subdirEF := &repb.Directory{
		Files: []*repb.FileNode{
			&repb.FileNode{Name: "e", Digest: fileDigests["e"]},
			&repb.FileNode{Name: "f", Digest: fileDigests["f"]},
		},
	}
	subdirGH := &repb.Directory{
		Files: []*repb.FileNode{
			&repb.FileNode{Name: "g", Digest: fileDigests["g"]},
			&repb.FileNode{Name: "h", Digest: fileDigests["h"]},
		},
	}
	abDigest, err := cachetools.UploadProto(ctx, bsProxy, "", repb.DigestFunction_SHA256, subdirAB)
	require.NoError(b, err)
	cdDigest, err := cachetools.UploadProto(ctx, bsProxy, "", repb.DigestFunction_SHA256, subdirCD)
	require.NoError(b, err)
	efDigest, err := cachetools.UploadProto(ctx, bsProxy, "", repb.DigestFunction_SHA256, subdirEF)
	require.NoError(b, err)
	ghDigest, err := cachetools.UploadProto(ctx, bsProxy, "", repb.DigestFunction_SHA256, subdirGH)
	require.NoError(b, err)

	subdirABCD := &repb.Directory{
		Directories: []*repb.DirectoryNode{
			&repb.DirectoryNode{Name: "ab", Digest: abDigest},
			&repb.DirectoryNode{Name: "cd", Digest: cdDigest},
		},
	}
	subdirEFGH := &repb.Directory{
		Directories: []*repb.DirectoryNode{
			&repb.DirectoryNode{Name: "ef", Digest: efDigest},
			&repb.DirectoryNode{Name: "gh", Digest: ghDigest},
		},
	}
	abcdDigest, err := cachetools.UploadProto(ctx, bsProxy, "", repb.DigestFunction_SHA256, subdirABCD)
	require.NoError(b, err)
	efghDigest, err := cachetools.UploadProto(ctx, bsProxy, "", repb.DigestFunction_SHA256, subdirEFGH)
	require.NoError(b, err)

	root := &repb.Directory{
		Directories: []*repb.DirectoryNode{
			&repb.DirectoryNode{Name: "abcd", Digest: abcdDigest},
			&repb.DirectoryNode{Name: "efgh", Digest: efghDigest},
		},
	}
	rootDigest, err := cachetools.UploadProto(ctx, bsProxy, "", repb.DigestFunction_SHA256, root)
	require.NoError(b, err)

	files := []string{"a", "b", "c", "d", "e", "f", "g", "h", "ab", "cd", "ef", "gh", "abcd", "efgh"}

	// Warm up tree cache
	treeFiles := cas.ReadTree(ctx, b, casProxy, "", rootDigest)
	require.ElementsMatch(b, files, treeFiles)
	unaryRequests.Store(0)
	streamRequests.Store(0)

	b.ReportAllocs()

	for b.Loop() {
		treeFiles := cas.ReadTree(ctx, b, casProxy, "", rootDigest)
		require.ElementsMatch(b, files, treeFiles)
		require.Equal(b, int32(0), unaryRequests.Load())
		require.Equal(b, int32(0), streamRequests.Load())
	}

}

func TestFindMissingBlobs_QuorumBypassesPresenceCache(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", 30*time.Second)
	ctx := testContext()
	conn, requestCount, _ := runRemoteCASS(ctx, testenv.GetTestEnv(t), t)
	proxyEnv := testenv.GetTestEnv(t)
	proxyEnv.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	proxyEnv.SetContentAddressableStorageClient(repb.NewContentAddressableStorageClient(conn))
	require.NoError(t, atime_updater.Register(proxyEnv))
	proxy := repb.NewContentAddressableStorageClient(runCASProxy(ctx, conn, proxyEnv, t))
	ctx = metadata.AppendToOutgoingContext(ctx, authutil.APIKeyHeader, "US1")
	d := digestProto(barDigest, 3)
	update(ctx, proxy, map[*repb.Digest]string{d: "bar"}, t)
	requestCount.Store(0)
	findMissing(ctx, proxy, []*repb.Digest{d}, nil, t)
	findMissing(ctx, proxy, []*repb.Digest{d}, nil, t)
	require.Equal(t, int32(1), requestCount.Load())
	// A cached presence result cannot establish or refresh replica quorum.
	for i := int32(2); i <= 4; i++ {
		findMissing(findmissing.WithQuorum(ctx), proxy, []*repb.Digest{d}, nil, t)
		require.Equal(t, i, requestCount.Load())
	}
}

// A presence check must keep a hot blob ahead of cold blobs in the backing LRU.
func TestPresenceLifetime_ProxyRefreshBeforeEviction(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_refresh_interval", time.Second)
	for _, ttl := range []time.Duration{0, time.Minute} {
		t.Run(ttl.String(), func(t *testing.T) {
			flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", ttl)
			ctx := testContext()
			remoteEnv := testenv.GetTestEnv(t)
			backing, err := memory_cache.NewMemoryCache(6) // two three-byte blobs
			require.NoError(t, err)
			remoteEnv.SetCache(backing)
			conn, _, _ := runRemoteCASS(ctx, remoteEnv, t)
			remote := repb.NewContentAddressableStorageClient(conn)
			proxyEnv := testenv.GetTestEnv(t)
			clock := clockwork.NewFakeClockAt(time.Unix(1700000000, 0))
			proxyEnv.SetClock(clock)
			proxyEnv.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
			proxyEnv.SetContentAddressableStorageClient(remote)
			require.NoError(t, atime_updater.Register(proxyEnv))
			proxy := repb.NewContentAddressableStorageClient(runCASProxy(ctx, conn, proxyEnv, t))
			ctx = metadata.AppendToOutgoingContext(ctx, authutil.APIKeyHeader, "US1")
			hot := digestProto(fooDigest, 3)
			cold := digestProto(barDigest, 3)
			next := digestProto(bazDigest, 3)
			// Write remotely so no local data copy can mask a backing-cache miss.
			update(ctx, remote, map[*repb.Digest]string{hot: "foo"}, t)
			findMissing(ctx, proxy, []*repb.Digest{hot}, nil, t)
			update(ctx, remote, map[*repb.Digest]string{cold: "bar"}, t)
			clock.Advance(time.Second)
			findMissing(ctx, proxy, []*repb.Digest{hot}, nil, t)
			// With a real refresh, cold is now LRU. A skipped refresh evicts hot.
			update(ctx, remote, map[*repb.Digest]string{next: "baz"}, t)
			findMissing(ctx, proxy, []*repb.Digest{hot}, nil, t)
			rsp, err := remote.BatchReadBlobs(ctx, readBlobsRequest([]*repb.Digest{hot}))
			require.NoError(t, err)
			require.Len(t, rsp.GetResponses(), 1)
			require.Equal(t, int32(codes.OK), rsp.GetResponses()[0].GetStatus().GetCode(),
				"proxy reported present immediately before backend read; TTL=%s", ttl)
			require.Equal(t, []byte("foo"), rsp.GetResponses()[0].GetData())
		})
	}
}

// A delayed presence response must not undo a newer backend read miss.
// The callback orders the two overlapping RPC responses without sleeps.
type delayedPresenceClient struct {
	repb.ContentAddressableStorageClient
	beforePresenceResponse func()
	calls                  int
}

func (c *delayedPresenceClient) FindMissingBlobs(ctx context.Context, req *repb.FindMissingBlobsRequest, opts ...grpc.CallOption) (*repb.FindMissingBlobsResponse, error) {
	c.calls++
	if c.calls == 1 {
		c.beforePresenceResponse()
		return &repb.FindMissingBlobsResponse{}, nil // Snapshot taken before eviction.
	}
	return &repb.FindMissingBlobsResponse{MissingBlobDigests: req.GetBlobDigests()}, nil
}

func (c *delayedPresenceClient) BatchReadBlobs(ctx context.Context, req *repb.BatchReadBlobsRequest, opts ...grpc.CallOption) (*repb.BatchReadBlobsResponse, error) {
	return nil, status.Error(codes.NotFound, "blob evicted after presence snapshot")
}

func TestPresenceInvalidation_DelayedLookupCannotResurrect(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked=%t", chunked), func(t *testing.T) {
			flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Hour)
			ctx := testContext()
			if chunked {
				ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(cdc.ChunkedHeaderName, cdc.ChunkedHeaderValue))
				require.True(t, cdc.IsChunked(ctx))
			}
			env := testenv.GetTestEnv(t)
			env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
			env.SetLocalCASServer(runLocalCASS(ctx, env, t))
			remote := &delayedPresenceClient{}
			env.SetContentAddressableStorageClient(remote)
			proxy, err := New(env)
			require.NoError(t, err)
			d := digestProto(fooDigest, 3)
			remote.beforePresenceResponse = func() {
				_, err := proxy.batchReadBlobsRemote(ctx, readBlobsRequest([]*repb.Digest{d}))
				require.Equal(t, codes.NotFound, status.Code(err))
			}
			_, err = proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{d}))
			require.NoError(t, err)
			rsp, err := proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{d}))
			require.NoError(t, err)
			require.Equal(t, []*repb.Digest{d}, rsp.GetMissingBlobDigests(), "older presence response resurrected an entry after a backend NotFound")
		})
	}
}

func TestPresenceLifetime_DefaultPreservesConfiguredTTL(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Hour)
	ctx := testContext()
	env := testenv.GetTestEnv(t)
	clock := clockwork.NewFakeClockAt(time.Unix(1700000000, 0))
	env.SetClock(clock)
	env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	env.SetLocalCASServer(runLocalCASS(ctx, env, t))
	remote := &delayedPresenceClient{beforePresenceResponse: func() {}}
	env.SetContentAddressableStorageClient(remote)
	proxy, err := New(env)
	require.NoError(t, err)
	d := digestProto(fooDigest, 3)
	_, err = proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{d}))
	require.NoError(t, err)
	clock.Advance(time.Minute)
	rsp, err := proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{d}))
	require.NoError(t, err)
	require.Empty(t, rsp.GetMissingBlobDigests(), "default must preserve the configured hour TTL")
	clock.Advance(time.Hour)
	rsp, err = proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{d}))
	require.NoError(t, err)
	require.Equal(t, []*repb.Digest{d}, rsp.GetMissingBlobDigests())
}

// Unidentified descendants must not cause a scan or clear unrelated entries.
func (c *delayedPresenceClient) GetTree(ctx context.Context, req *repb.GetTreeRequest, opts ...grpc.CallOption) (repb.ContentAddressableStorage_GetTreeClient, error) {
	return nil, status.Error(codes.NotFound, "directory missing")
}

func TestGetTree_NotFoundInvalidatesOnlyRoot(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Hour)
	ctx := testContext()
	env := testenv.GetTestEnv(t)
	env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	env.SetLocalCASServer(runLocalCASS(ctx, env, t))
	remote := &delayedPresenceClient{beforePresenceResponse: func() {}}
	env.SetContentAddressableStorageClient(remote)
	proxy, err := New(env)
	require.NoError(t, err)
	root := digestProto(fooDigest, 3)
	other := digestProto(barDigest, 3)
	_, err = proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{root, other}))
	require.NoError(t, err)
	err = proxy.getTreeWithoutCaching(&repb.GetTreeRequest{RootDigest: root, DigestFunction: repb.DigestFunction_SHA256}, &presenceTreeStream{ctx: ctx})
	require.Equal(t, codes.NotFound, status.Code(err))
	rsp, err := proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{root, other}))
	require.NoError(t, err)
	require.Equal(t, []*repb.Digest{root}, rsp.GetMissingBlobDigests())
}

type presenceTreeStream struct {
	repb.ContentAddressableStorage_GetTreeServer
	ctx context.Context
}

func (s *presenceTreeStream) Context() context.Context { return s.ctx }

// This client always has the requested blob, but can overlap each presence
// lookup with a read miss for a different digest. No sleeps or random ordering.
type unrelatedInvalidationClient struct {
	repb.ContentAddressableStorageClient
	beforeResponse func()
	calls          int
}

func (c *unrelatedInvalidationClient) FindMissingBlobs(ctx context.Context, req *repb.FindMissingBlobsRequest, opts ...grpc.CallOption) (*repb.FindMissingBlobsResponse, error) {
	c.calls++
	if c.beforeResponse != nil {
		c.beforeResponse()
	}
	return &repb.FindMissingBlobsResponse{}, nil
}
func (c *unrelatedInvalidationClient) BatchReadBlobs(ctx context.Context, req *repb.BatchReadBlobsRequest, opts ...grpc.CallOption) (*repb.BatchReadBlobsResponse, error) {
	return nil, status.Error(codes.NotFound, "unrelated blob missing")
}

// An unrelated read miss must not disable caching for a healthy hot digest.
func TestPresenceInvalidation_UnrelatedMissesDoNotStarveFills(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked=%t", chunked), func(t *testing.T) {
			flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Hour)
			ctx := testContext()
			if chunked {
				ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(cdc.ChunkedHeaderName, cdc.ChunkedHeaderValue))
			}
			env := testenv.GetTestEnv(t)
			env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
			env.SetLocalCASServer(runLocalCASS(ctx, env, t))
			remote := &unrelatedInvalidationClient{}
			env.SetContentAddressableStorageClient(remote)
			proxy, err := New(env)
			require.NoError(t, err)
			remote.beforeResponse = func() {
				_, err := proxy.batchReadBlobsRemote(ctx, readBlobsRequest([]*repb.Digest{digestProto(barDigest, 3)}))
				require.Equal(t, codes.NotFound, status.Code(err))
			}
			req := findMissingBlobsRequest([]*repb.Digest{digestProto(fooDigest, 3)})
			for range 20 {
				rsp, err := proxy.FindMissingBlobs(ctx, req)
				require.NoError(t, err)
				require.Empty(t, rsp.GetMissingBlobDigests())
			}
			during := remote.calls
			// Once invalidations stop, one lookup must populate the cache again.
			remote.beforeResponse = nil
			for range 20 {
				_, err := proxy.FindMissingBlobs(ctx, req)
				require.NoError(t, err)
			}
			t.Logf("deterministic schedule: backend calls during invalidations=%d/20; after=%d/20", during, remote.calls-during)
			require.LessOrEqual(t, remote.calls-during, 1, "fills must recover when invalidations stop")
			require.Equal(t, 1, during, "unrelated missing digest prevented healthy digest from being cached")
		})
	}
}

func TestPresenceLookupTracking_BoundedAndReleased(t *testing.T) {
	s := &CASServerProxy{}
	// Saturate every shard without evicting active lookup records.
	for i := range s.presenceShards {
		sh := &s.presenceShards[i]
		sh.lookups = make(map[string]*presenceLookup)
		for j := range presenceShardCapacity {
			sh.lookups[fmt.Sprint(j)] = &presenceLookup{refs: 1}
		}
	}
	key := digest.NewKey(&repb.Digest{Hash: "a", SizeBytes: 1})
	before := testutil.ToFloat64(metrics.FindMissingBlobsTrackingSaturation)
	require.Empty(t, s.beginPresenceLookups(map[digest.Key]string{key: "overflow"}))
	require.Equal(t, before+1, testutil.ToFloat64(metrics.FindMissingBlobsTrackingSaturation))
	sh := s.presenceShard("0")
	require.Len(t, sh.lookups, presenceShardCapacity)
	snapshot := s.beginPresenceLookups(map[digest.Key]string{key: "0"})
	require.Contains(t, snapshot, "0")
	sh.lookups["0"].sequence++
	require.NotEqual(t, snapshot["0"], sh.lookups["0"].sequence)
	s.endPresenceLookups(snapshot)
	require.Equal(t, 1, sh.lookups["0"].refs)
	s.endPresenceLookups(map[string]uint64{"0": 0})
	require.NotContains(t, sh.lookups, "0")
	snapshot = s.beginPresenceLookups(map[digest.Key]string{key: "0"})
	require.Contains(t, snapshot, "0")
	s.endPresenceLookups(snapshot)
	require.NotContains(t, sh.lookups, "0")
}

// A lagging replica (or an upstream presence cache) can answer from a snapshot
// older than a completed read miss, even when the presence RPC starts later.
func TestPresenceInvalidation_LaterLookupWithStaleBackendSnapshot(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Hour)
	ctx := testContext()
	env := testenv.GetTestEnv(t)
	env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	env.SetLocalCASServer(runLocalCASS(ctx, env, t))
	remote := &delayedPresenceClient{beforePresenceResponse: func() {}}
	env.SetContentAddressableStorageClient(remote)
	proxy, err := New(env)
	require.NoError(t, err)
	d := digestProto(fooDigest, 3)
	// No FindMissing RPC is active when this authoritative read fails.
	_, err = proxy.batchReadBlobsRemote(ctx, readBlobsRequest([]*repb.Digest{d}))
	require.Equal(t, codes.NotFound, status.Code(err))
	// The first presence response is stale. The backend has converged by the
	// second request; the proxy should not extend the stale answer for an hour.
	_, err = proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{d}))
	require.NoError(t, err)
	rsp, err := proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{d}))
	require.NoError(t, err)
	require.Equal(t, []*repb.Digest{d}, rsp.GetMissingBlobDigests(), "post-invalidation stale snapshot was cached for the configured TTL")
}

// A recovered blob is passed through during suppression, then cached normally
// at the window boundary. Repeated invalidations extend the window.
func TestPresenceTombstone_ExpiryAndRenewal(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked=%t", chunked), func(t *testing.T) {
			flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Hour)
			flags.Set(t, "cache_proxy.find_missing_blobs_tombstone_ttl", 3*time.Minute)
			ctx := testContext()
			if chunked {
				ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(cdc.ChunkedHeaderName, cdc.ChunkedHeaderValue))
			}
			env := testenv.GetTestEnv(t)
			clock := clockwork.NewFakeClock()
			env.SetClock(clock)
			env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
			env.SetLocalCASServer(runLocalCASS(ctx, env, t))
			remote := &delayedPresenceClient{beforePresenceResponse: func() {}}
			env.SetContentAddressableStorageClient(remote)
			proxy, err := New(env)
			require.NoError(t, err)
			d := digestProto(fooDigest, 3)
			rn := digest.NewCASResourceName(d, "", repb.DigestFunction_SHA256)
			key := proxy.findMissingBlobsCacheKey("GR1", findMissingBlobsRequest([]*repb.Digest{d}), d)
			proxy.InvalidatePresence(ctx, rn)
			check := func(cached bool) {
				// This client returns a positive snapshot once, then missing; reset it so
				// every backend lookup in this test reports a recovered blob.
				remote.calls = 0
				remote.beforePresenceResponse = func() {}
				rsp, err := proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{d}))
				require.NoError(t, err)
				require.Empty(t, rsp.GetMissingBlobDigests())
				require.Equal(t, cached, proxy.findMissingBlobsCache.Contains(key) || proxy.findMissingChunksCache.Contains(key))
			}
			check(false)
			clock.Advance(2 * time.Minute)
			proxy.InvalidatePresence(ctx, rn)
			clock.Advance(time.Minute)
			check(false)
			clock.Advance(2 * time.Minute)
			check(true)
		})
	}
}

func TestPresenceTombstone_BoundedLRU(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Hour)
	ctx := testContext()
	env := testenv.GetTestEnv(t)
	env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	env.SetLocalCASServer(runLocalCASS(ctx, env, t))
	env.SetContentAddressableStorageClient(&delayedPresenceClient{})
	proxy, err := New(env)
	require.NoError(t, err)
	// Generate deterministic colliding keys to exercise a single shard's limit.
	var keys []string
	var rns []*digest.CASResourceName
	for i := 0; len(keys) < presenceShardCapacity+1; i++ {
		d := &repb.Digest{Hash: fmt.Sprintf("%064x", i), SizeBytes: 1}
		rn := digest.NewCASResourceName(d, "", repb.DigestFunction_SHA256)
		key := "GR1/" + rn.DownloadString()
		if proxy.presenceShard(key) != &proxy.presenceShards[0] {
			continue
		}
		keys = append(keys, key)
		rns = append(rns, rn)
	}
	for _, rn := range rns[:presenceShardCapacity] {
		proxy.InvalidatePresence(ctx, rn)
	}
	sh := &proxy.presenceShards[0]
	require.True(t, sh.tombstoned(keys[0], proxy.now())) // touch oldest
	proxy.InvalidatePresence(ctx, rns[presenceShardCapacity])
	require.Len(t, sh.tombstones, presenceShardCapacity)
	require.Equal(t, presenceShardCapacity, sh.order.Len())
	require.Contains(t, sh.tombstones, keys[0])
	require.NotContains(t, sh.tombstones, keys[1])
	require.Empty(t, sh.lookups)
}

func TestPresenceLookupTracking_OnlyMisses(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Hour)
	ctx := testContext()
	env := testenv.GetTestEnv(t)
	env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	env.SetLocalCASServer(runLocalCASS(ctx, env, t))
	remote := &delayedPresenceClient{beforePresenceResponse: func() {}}
	env.SetContentAddressableStorageClient(remote)
	proxy, err := New(env)
	require.NoError(t, err)
	hit := digestProto(fooDigest, 3)
	miss := digestProto(barDigest, 3)
	_, err = proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{hit}))
	require.NoError(t, err)
	remote.calls = 0
	remote.beforePresenceResponse = func() {
		hitKey := proxy.findMissingBlobsCacheKey("GR1", findMissingBlobsRequest(nil), hit)
		missKey := proxy.findMissingBlobsCacheKey("GR1", findMissingBlobsRequest(nil), miss)
		require.NotContains(t, proxy.presenceShard(hitKey).lookups, hitKey)
		require.Contains(t, proxy.presenceShard(missKey).lookups, missKey)
		count := 0
		for i := range proxy.presenceShards {
			count += len(proxy.presenceShards[i].lookups)
		}
		require.Equal(t, 1, count)
	}
	_, err = proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{hit, miss, hit, miss}))
	require.NoError(t, err)
	for i := range proxy.presenceShards {
		require.Empty(t, proxy.presenceShards[i].lookups)
	}
}

// Expiring tombstones must not weaken the sequence guard for an older RPC.
func TestPresenceInvalidation_OverlapOutlivesTombstone(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Hour)
	flags.Set(t, "cache_proxy.find_missing_blobs_tombstone_ttl", time.Minute)
	ctx := testContext()
	env := testenv.GetTestEnv(t)
	clock := clockwork.NewFakeClock()
	env.SetClock(clock)
	env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	env.SetLocalCASServer(runLocalCASS(ctx, env, t))
	remote := &delayedPresenceClient{}
	env.SetContentAddressableStorageClient(remote)
	proxy, err := New(env)
	require.NoError(t, err)
	d := digestProto(fooDigest, 3)
	remote.beforePresenceResponse = func() {
		proxy.InvalidatePresence(ctx, digest.NewCASResourceName(d, "", repb.DigestFunction_SHA256))
		clock.Advance(2 * time.Minute)
	}
	_, err = proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{d}))
	require.NoError(t, err)
	rsp, err := proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{d}))
	require.NoError(t, err)
	require.Equal(t, []*repb.Digest{d}, rsp.GetMissingBlobDigests())
}
