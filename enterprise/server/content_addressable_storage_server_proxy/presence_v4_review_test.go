package content_addressable_storage_server_proxy

import (
	"context"
	"fmt"
	"testing"
	"time"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type reviewPresenceClient struct {
	repb.ContentAddressableStorageClient
	beforeResponse func()
	calls          int
}

func (c *reviewPresenceClient) FindMissingBlobs(ctx context.Context, req *repb.FindMissingBlobsRequest, opts ...grpc.CallOption) (*repb.FindMissingBlobsResponse, error) {
	c.calls++
	if c.calls == 1 {
		c.beforeResponse()
		return &repb.FindMissingBlobsResponse{}, nil
	}
	return &repb.FindMissingBlobsResponse{MissingBlobDigests: req.GetBlobDigests()}, nil
}
func (c *reviewPresenceClient) BatchReadBlobs(context.Context, *repb.BatchReadBlobsRequest, ...grpc.CallOption) (*repb.BatchReadBlobsResponse, error) {
	return nil, status.Error(codes.NotFound, "missing")
}

// Exercise the sequence guard through RPC methods while invalidation churn
// exceeds the entire tombstone budget. No private tracking fields are used:
// this test also runs against the original implementation.
func TestReviewPresence_OverlapSurvivesTombstonePressure(t *testing.T) {
	flags.Set(t, "cache_proxy.find_missing_blobs_cache_ttl", time.Hour)
	ctx := testauth.WithAuthenticatedUserInfo(testContext(), testauth.TestUsers("US1", "GR1")["US1"])
	env := testenv.GetTestEnv(t)
	env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	env.SetLocalCASServer(runLocalCASS(ctx, env, t))
	remote := &reviewPresenceClient{}
	env.SetContentAddressableStorageClient(remote)
	proxy, err := New(env)
	require.NoError(t, err)
	d := digestProto(fooDigest, 3)
	remote.beforeResponse = func() {
		// The target is invalidated first, making its tombstone oldest. Every
		// shard receives more than 1,000 subsequent invalidations.
		ds := []*repb.Digest{d}
		for i := 0; i < 200000; i++ {
			ds = append(ds, &repb.Digest{Hash: fmt.Sprintf("%064x", i), SizeBytes: 1})
		}
		_, err := proxy.batchReadBlobsRemote(ctx, readBlobsRequest(ds))
		require.Equal(t, codes.NotFound, status.Code(err))
	}
	_, err = proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{d}))
	require.NoError(t, err)
	rsp, err := proxy.FindMissingBlobs(ctx, findMissingBlobsRequest([]*repb.Digest{d}))
	require.NoError(t, err)
	require.Equal(t, []*repb.Digest{d}, rsp.GetMissingBlobDigests(), "in-flight stale response was cached after invalidation churn")
	require.Equal(t, 2, remote.calls)
	t.Log("deterministic schedule; no random seed; 200,000 distinct invalidations")
}
