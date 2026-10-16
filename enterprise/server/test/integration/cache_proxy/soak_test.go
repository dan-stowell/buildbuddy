package cache_proxy_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/backends/pebble_cache"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/hit_tracker_client"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/hit_tracker_service"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/test/integration/remote_execution/rbetest"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/cachetools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testkeys"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/authutil"
	"github.com/buildbuddy-io/buildbuddy/server/util/claims"
	"github.com/buildbuddy-io/buildbuddy/server/util/grpc_server"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TestCacheProxyResourceSoak runs a mixed cache workload through a cache proxy
// (with a pebble local cache, remote hit tracking and atime updates) in front
// of an app, and fails if any group of goroutines or file descriptors grows
// steadily over the batches (see testleak.Soak). It takes a few minutes, so it
// is tagged manual:
//
//	bb remote test --config=remote //enterprise/server/test/integration/cache_proxy:soak_test --test_output=all --test_arg=-test.v
//
// Set --test_env=SOAK_BATCHES=40 for a longer run (default 10).
//
// --test_env=SOAK_INJECT_LEAK=1 leaks a goroutine and a file descriptor per
// batch, which must fail the test with exactly those two groups.
func TestCacheProxyResourceSoak(t *testing.T) {
	flags.Set(t, "grpc_server_worker_multiplier", 0)
	flags.Set(t, "grpc_client.pool_size", 2)
	flags.Set(t, "cache_proxy.remote_hit_tracker.workers", 3)
	flags.Set(t, "cache_proxy.remote_hit_tracker.update_interval", 100*time.Millisecond)
	flags.Set(t, "cache_proxy.remote_atime_update_interval", 100*time.Millisecond)
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Second)
	keys := testkeys.GenerateES256KeyPair(t)
	flags.Set(t, "auth.jwt_es256_private_key", keys.PrivateKeyPEM)
	flags.Set(t, "auth.remote.use_es256_jwts", true)
	require.NoError(t, claims.Init())
	var hits atomic.Int64
	interceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if strings.HasSuffix(info.FullMethod, "/Track") {
			hits.Add(1)
		}
		if r, ok := req.(*repb.GetActionResultRequest); ok && strings.HasSuffix(r.GetInstanceName(), "-error") {
			return nil, status.UnavailableError("soak injected backend error")
		}
		return handler(ctx, req)
	}
	rbe := rbetest.NewRBETestEnv(t)
	app := rbe.AddBuildBuddyServerWithOptions(&rbetest.BuildBuddyServerOptions{
		EnvModifier: func(env *testenv.TestEnv) {
			require.NoError(t, hit_tracker_service.Register(env))
		},
		GRPCServerConfig: grpc_server.GRPCServerConfig{
			ExtraChainedUnaryInterceptors: []grpc.UnaryServerInterceptor{interceptor},
		},
	})
	flags.Set(t, "cache_proxy.remote_cache", app.GRPCAddress())
	proxy := rbe.AddCacheProxyWithOptions(&rbetest.CacheProxyOptions{EnvModifier: func(env *testenv.TestEnv) {
		threshold := time.Duration(0)
		pc, err := pebble_cache.NewPebbleCache(env, &pebble_cache.Options{
			RootDirectory:        t.TempDir(),
			MaxSizeBytes:         1_000_000_000,
			AtimeUpdateThreshold: &threshold,
		})
		require.NoError(t, err)
		require.NoError(t, pc.Start())
		t.Cleanup(func() { require.NoError(t, pc.Stop()) })
		env.SetCache(pc)
		require.NoError(t, hit_tracker_client.Register(env))
	}})
	cas := proxy.GetContentAddressableStorageClient()
	bs := proxy.GetByteStreamClient()
	ac := repb.NewActionCacheClient(proxy.Conn())
	ctx := metadata.AppendToOutgoingContext(t.Context(), authutil.APIKeyHeader, rbe.APIKey1)
	injectLeak := os.Getenv("SOAK_INJECT_LEAK") == "1"
	stop := make(chan struct{})
	defer close(stop)
	batches := 10
	if value := os.Getenv("SOAK_BATCHES"); value != "" {
		var err error
		batches, err = strconv.Atoi(value)
		require.NoError(t, err, "SOAK_BATCHES must be an integer")
		require.GreaterOrEqual(t, batches, 4)
	}
	testleak.Soak(t, 2, batches, func(batch int) {
		t.Logf("batch %d: Redis pool stats: %+v", batch, rbe.GetRedisClient().PoolStats())
		instance := fmt.Sprintf("soak/%d", batch)
		small := []byte(fmt.Sprintf("small-%d", batch))
		d, err := digest.Compute(bytes.NewReader(small), repb.DigestFunction_SHA256)
		require.NoError(t, err)
		missing, err := cas.FindMissingBlobs(ctx, &repb.FindMissingBlobsRequest{InstanceName: instance, BlobDigests: []*repb.Digest{d}})
		require.NoError(t, err)
		require.Len(t, missing.MissingBlobDigests, 1)
		up, err := cas.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{InstanceName: instance, Requests: []*repb.BatchUpdateBlobsRequest_Request{{Digest: d, Data: small}}})
		require.NoError(t, err)
		require.Zero(t, up.Responses[0].Status.Code)
		for range 2 {
			read, err := cas.BatchReadBlobs(ctx, &repb.BatchReadBlobsRequest{InstanceName: instance, Digests: []*repb.Digest{d}})
			require.NoError(t, err)
			require.Equal(t, small, read.Responses[0].Data)
			missing, err = cas.FindMissingBlobs(ctx, &repb.FindMissingBlobsRequest{InstanceName: instance, BlobDigests: []*repb.Digest{d}})
			require.NoError(t, err)
			require.Empty(t, missing.MissingBlobDigests)
		}
		for _, compressor := range []repb.Compressor_Value{repb.Compressor_IDENTITY, repb.Compressor_ZSTD} {
			data := make([]byte, 3<<20)
			_, err := rand.Read(data)
			require.NoError(t, err)
			large, err := digest.Compute(bytes.NewReader(data), repb.DigestFunction_SHA256)
			require.NoError(t, err)
			rn := digest.NewCASResourceName(large, instance, repb.DigestFunction_SHA256)
			rn.SetCompressor(compressor)
			_, _, err = cachetools.UploadFromReader(ctx, bs, rn, bytes.NewReader(data))
			require.NoError(t, err)
			var out bytes.Buffer
			require.NoError(t, cachetools.GetBlob(ctx, bs, rn, &out))
			require.Equal(t, data, out.Bytes())
			cctx, cancel := context.WithCancel(ctx)
			stream, err := bs.Read(cctx, &bspb.ReadRequest{ResourceName: rn.DownloadString()})
			require.NoError(t, err)
			_, err = stream.Recv()
			require.NoError(t, err)
			cancel()
			for err == nil {
				_, err = stream.Recv()
			}
			cctx, cancel = context.WithCancel(ctx)
			w, err := bs.Write(cctx)
			require.NoError(t, err)
			require.NoError(t, w.Send(&bspb.WriteRequest{ResourceName: digest.NewCASResourceName(large, instance, repb.DigestFunction_SHA256).NewUploadString(), Data: data[:1024]}))
			cancel()
			_, _ = w.CloseAndRecv()
		}
		_, err = ac.GetActionResult(ctx, &repb.GetActionResultRequest{InstanceName: instance, ActionDigest: d})
		require.True(t, status.IsNotFoundError(err), "%v", err)
		_, err = ac.UpdateActionResult(ctx, &repb.UpdateActionResultRequest{InstanceName: instance, ActionDigest: d, ActionResult: &repb.ActionResult{ExitCode: 0}})
		require.NoError(t, err)
		_, err = ac.GetActionResult(ctx, &repb.GetActionResultRequest{InstanceName: instance, ActionDigest: d})
		require.NoError(t, err)
		// Force an uncached backend request and a deterministic app-side failure.
		_, err = ac.GetActionResult(ctx, &repb.GetActionResultRequest{InstanceName: instance + "-error", ActionDigest: d})
		require.Error(t, err)
		root, err := cachetools.UploadProto(ctx, bs, instance, repb.DigestFunction_SHA256, &repb.Directory{Files: []*repb.FileNode{{Name: "small", Digest: d}}})
		require.NoError(t, err)
		tree, err := cas.GetTree(ctx, &repb.GetTreeRequest{InstanceName: instance, RootDigest: root})
		require.NoError(t, err)
		for {
			_, err = tree.Recv()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
		}
		if injectLeak {
			go func() { <-stop }()
			f, err := os.Open("/dev/null")
			require.NoError(t, err)
			t.Cleanup(func() { f.Close() })
		}
	})
	t.Logf("final Redis pool stats: %+v", rbe.GetRedisClient().PoolStats())
	require.Positive(t, hits.Load(), "remote hit tracker must reach the app")
}
