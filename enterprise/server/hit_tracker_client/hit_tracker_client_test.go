package hit_tracker_client

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/proxy_util"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/metrics"
	"github.com/buildbuddy-io/buildbuddy/server/real_environment"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/authutil"
	"github.com/buildbuddy-io/buildbuddy/server/util/bazel_request"
	"github.com/buildbuddy-io/buildbuddy/server/util/log"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/buildbuddy-io/buildbuddy/server/util/usageutil"
	"github.com/jonboulle/clockwork"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	capb "github.com/buildbuddy-io/buildbuddy/proto/cache"
	hitpb "github.com/buildbuddy-io/buildbuddy/proto/hit_tracker"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	rspb "github.com/buildbuddy-io/buildbuddy/proto/resource"
)

const (
	user1  = "USER1"
	group1 = "GROUP1"
)

var (
	aDigest = digestProto(strings.Repeat("a", 64), 3)
	bDigest = digestProto(strings.Repeat("b", 64), 3)
	cDigest = digestProto(strings.Repeat("c", 64), 3)
	dDigest = digestProto(strings.Repeat("d", 64), 3)
	eDigest = digestProto(strings.Repeat("e", 64), 3)
	fDigest = digestProto(strings.Repeat("f", 64), 3)
	gDigest = digestProto(strings.Repeat("g", 64), 3)
	hDigest = digestProto(strings.Repeat("h", 64), 3)
	iDigest = digestProto(strings.Repeat("i", 64), 3)
	jDigest = digestProto(strings.Repeat("j", 64), 3)
	kDigest = digestProto(strings.Repeat("k", 64), 3)

	anonKey = usageutil.EncodeCollection(&usageutil.Collection{
		GroupID: interfaces.AuthAnonymousUser,
	})
	group1Key = usageutil.EncodeCollection(&usageutil.Collection{
		GroupID: group1,
	})
	group1InternalKey = usageutil.EncodeCollection(&usageutil.Collection{
		GroupID: group1,
		Origin:  "internal",
	})
	group1InternalBazelKey = usageutil.EncodeCollection(&usageutil.Collection{
		GroupID: group1,
		Client:  "bazel",
		Origin:  "internal",
	})
)

func digestProto(hash string, sizeBytes int64) *repb.Digest {
	return &repb.Digest{Hash: hash, SizeBytes: sizeBytes}
}

type testHitTracker struct {
	t                  testing.TB
	authenticator      interfaces.Authenticator
	wg                 sync.WaitGroup
	casDownloads       map[string]*atomic.Int64
	casBytesDownloaded map[string]*atomic.Int64
	casUploads         map[string]*atomic.Int64
	casBytesUploaded   map[string]*atomic.Int64
	acDownloads        map[string]*atomic.Int64
	acBytesDownloaded  map[string]*atomic.Int64
	acUploads          map[string]*atomic.Int64
	acBytesUploaded    map[string]*atomic.Int64

	// trackCalls counts Track RPCs that reached the handler.
	trackCalls atomic.Int64
	// The next len(injectedErrs) Track RPCs fail with these errors, in
	// order, without recording any hits.
	errMu        sync.Mutex
	injectedErrs []error

	// If set, called after a Track RPC has recorded its hits and before it
	// returns.
	afterRecord atomic.Pointer[func()]
}

func (ht *testHitTracker) failNextTracks(errs ...error) {
	ht.errMu.Lock()
	defer ht.errMu.Unlock()
	ht.injectedErrs = append(ht.injectedErrs, errs...)
}

func newTestHitTracker(t testing.TB, authenticator interfaces.Authenticator) *testHitTracker {
	out := &testHitTracker{
		t:                  t,
		authenticator:      authenticator,
		casDownloads:       map[string]*atomic.Int64{},
		casBytesDownloaded: map[string]*atomic.Int64{},
		casUploads:         map[string]*atomic.Int64{},
		casBytesUploaded:   map[string]*atomic.Int64{},
		acDownloads:        map[string]*atomic.Int64{},
		acBytesDownloaded:  map[string]*atomic.Int64{},
		acUploads:          map[string]*atomic.Int64{},
		acBytesUploaded:    map[string]*atomic.Int64{},
	}
	for _, k := range []string{anonKey, group1Key, group1InternalKey, group1InternalBazelKey} {
		out.casDownloads[k] = &atomic.Int64{}
		out.casBytesDownloaded[k] = &atomic.Int64{}
		out.casUploads[k] = &atomic.Int64{}
		out.casBytesUploaded[k] = &atomic.Int64{}
		out.acDownloads[k] = &atomic.Int64{}
		out.acBytesDownloaded[k] = &atomic.Int64{}
		out.acUploads[k] = &atomic.Int64{}
		out.acBytesUploaded[k] = &atomic.Int64{}
	}
	return out
}

func (ht *testHitTracker) Track(ctx context.Context, req *hitpb.TrackRequest) (*hitpb.TrackResponse, error) {
	ht.wg.Wait()
	ht.trackCalls.Add(1)
	ht.errMu.Lock()
	if len(ht.injectedErrs) > 0 {
		err := ht.injectedErrs[0]
		ht.injectedErrs = ht.injectedErrs[1:]
		ht.errMu.Unlock()
		return nil, err
	}
	ht.errMu.Unlock()

	key := usageutil.EncodeCollection(usageutil.CollectionFromRPCContext(ctx))

	for _, hit := range req.GetHits() {
		if hit.GetCacheRequestType() == capb.RequestType_READ {
			if hit.GetResource().GetCacheType() == rspb.CacheType_AC {
				ht.acDownloads[key].Add(1)
				ht.acBytesDownloaded[key].Add(hit.GetSizeBytes())
			} else {
				ht.casDownloads[key].Add(1)
				ht.casBytesDownloaded[key].Add(hit.GetSizeBytes())
			}
		} else if hit.GetCacheRequestType() == capb.RequestType_WRITE {
			if hit.GetResource().GetCacheType() == rspb.CacheType_AC {
				ht.acUploads[key].Add(1)
				ht.acBytesUploaded[key].Add(hit.GetSizeBytes())
			} else {
				ht.casUploads[key].Add(1)
				ht.casBytesUploaded[key].Add(hit.GetSizeBytes())
			}
		}
	}
	if f := ht.afterRecord.Load(); f != nil {
		(*f)()
	}
	return &hitpb.TrackResponse{}, nil
}

// pause blocks Track RPCs until the returned resume func is called. resume is
// also run at test cleanup, so a failed assertion cannot leave handlers, and
// with them the sender's shutdown, blocked forever.
func (ht *testHitTracker) pause(t testing.TB) (resume func()) {
	ht.wg.Add(1)
	var once sync.Once
	resume = func() { once.Do(ht.wg.Done) }
	t.Cleanup(resume)
	return resume
}

func (ht *testHitTracker) acDownloadExpectation(key string, expectation int64) func() bool {
	return func() bool {
		return ht.acDownloads[key] != nil && ht.acDownloads[key].Load() == expectation
	}
}

func (ht *testHitTracker) casDownloadExpectation(key string, expectation int64) func() bool {
	return func() bool {
		return ht.casDownloads[key] != nil && ht.casDownloads[key].Load() == expectation
	}
}

func setup(t testing.TB) (interfaces.Authenticator, *HitTrackerFactory, *testHitTracker) {
	return setupWithClock(t, nil)
}

// setupWithClock is like setup, but installs the given clock in the test env
// so that tests control when the hit-tracker-client sender ticks.
func setupWithClock(t testing.TB, clock clockwork.Clock) (interfaces.Authenticator, *HitTrackerFactory, *testHitTracker) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	te := testenv.GetTestEnv(t)
	if clock != nil {
		te.SetClock(clock)
	}
	authenticator := testauth.NewTestAuthenticator(t, testauth.TestUsers(user1, group1))
	te.SetAuthenticator(authenticator)
	hitTrackerService := newTestHitTracker(t, authenticator)
	grpcServer, runServer, lis := testenv.RegisterLocalGRPCServer(t, te)
	hitpb.RegisterHitTrackerServiceServer(grpcServer, hitTrackerService)
	go runServer()
	conn, err := testenv.LocalGRPCConn(t, ctx, lis)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return authenticator, newHitTrackerClient(ctx, te, conn), hitTrackerService
}

func authenticatedContext(user string, authenticator interfaces.Authenticator) context.Context {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(authutil.ClientIdentityHeaderName, "fakeheader"))
	if user != "" {
		ctx = authenticator.AuthContextFromAPIKey(ctx, user)
	}
	return authutil.ContextWithCachedAuthHeaders(ctx, authenticator)
}

func authenticatedContextWithInternalBazelClient(user string, authenticator interfaces.Authenticator) context.Context {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(authutil.ClientIdentityHeaderName, "fakeheader", usageutil.OriginHeaderName, "internal"))
	if user != "" {
		ctx = authenticator.AuthContextFromAPIKey(ctx, user)
	}
	return bazel_request.OverrideRequestMetadata(authutil.ContextWithCachedAuthHeaders(ctx, authenticator), &repb.RequestMetadata{
		ToolDetails: &repb.ToolDetails{
			ToolName: "bazel",
		},
	})
}

func authenticatedContextWithOrigin(user string, authenticator interfaces.Authenticator, origin string) context.Context {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(authutil.ClientIdentityHeaderName, "fakeheader", usageutil.OriginHeaderName, origin))
	if user != "" {
		ctx = authenticator.AuthContextFromAPIKey(ctx, user)
	}
	return authutil.ContextWithCachedAuthHeaders(ctx, authenticator)
}

func TestACHitTracker(t *testing.T) {
	_, hitTrackerFactory, hitTrackerService := setup(t)
	ctx := context.Background()
	hitTracker := hitTrackerFactory.NewACHitTracker(ctx, &repb.RequestMetadata{})
	hitTracker.TrackMiss(aDigest)
	hitTracker.TrackDownload(bDigest).CloseWithBytesTransferred(1000, 2000, repb.Compressor_IDENTITY, "test")
	hitTracker.TrackUpload(cDigest).CloseWithBytesTransferred(3000, 4000, repb.Compressor_IDENTITY, "test")

	time.Sleep(100 * time.Millisecond)

	// Hit tracking should be disabled by default
	require.Empty(t, int64(0), hitTrackerService.casDownloads)
	require.Empty(t, int64(0), hitTrackerService.casUploads)
	require.Empty(t, int64(0), hitTrackerService.acDownloads)
	require.Empty(t, int64(0), hitTrackerService.acUploads)
}

func TestACHitTracker_SkipRemote(t *testing.T) {
	_, hitTrackerFactory, hitTrackerService := setup(t)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(proxy_util.SkipRemoteKey, "true"))
	hitTracker := hitTrackerFactory.NewACHitTracker(ctx, &repb.RequestMetadata{})
	hitTracker.TrackMiss(aDigest)
	hitTracker.TrackDownload(bDigest).CloseWithBytesTransferred(1000, 2000, repb.Compressor_IDENTITY, "test")
	hitTracker.TrackUpload(cDigest).CloseWithBytesTransferred(3000, 4000, repb.Compressor_IDENTITY, "test")

	expectation := hitTrackerService.acDownloadExpectation(anonKey, 1)
	require.Eventually(t, expectation, 10*time.Second, 100*time.Millisecond)
	require.Equal(t, int64(2000), hitTrackerService.acBytesDownloaded[anonKey].Load())
	require.Equal(t, int64(1), hitTrackerService.acUploads[anonKey].Load())
	require.Equal(t, int64(4000), hitTrackerService.acBytesUploaded[anonKey].Load())
	require.Equal(t, int64(0), hitTrackerService.casDownloads[anonKey].Load())
	require.Equal(t, int64(0), hitTrackerService.casUploads[anonKey].Load())
}

func TestCASHitTracker(t *testing.T) {
	_, hitTrackerFactory, hitTrackerService := setup(t)
	ctx := context.Background()
	hitTracker := hitTrackerFactory.NewCASHitTracker(ctx, &repb.RequestMetadata{})
	hitTracker.TrackMiss(aDigest)
	hitTracker.TrackDownload(bDigest).CloseWithBytesTransferred(1000, 2000, repb.Compressor_IDENTITY, "test")
	hitTracker.TrackUpload(cDigest).CloseWithBytesTransferred(3000, 4000, repb.Compressor_IDENTITY, "test")

	expectation := hitTrackerService.casDownloadExpectation(anonKey, 1)
	require.Eventually(t, expectation, 10*time.Second, 100*time.Millisecond)
	require.Equal(t, int64(2000), hitTrackerService.casBytesDownloaded[anonKey].Load())
	// By default, we don't expect CAS upload tracking
	require.Equal(t, int64(0), hitTrackerService.casUploads[anonKey].Load())
}

func TestCASHitTracker_SkipRemote(t *testing.T) {
	_, hitTrackerFactory, hitTrackerService := setup(t)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(proxy_util.SkipRemoteKey, "true"))
	hitTracker := hitTrackerFactory.NewCASHitTracker(ctx, &repb.RequestMetadata{})
	hitTracker.TrackMiss(aDigest)
	hitTracker.TrackDownload(bDigest).CloseWithBytesTransferred(1000, 2000, repb.Compressor_IDENTITY, "test")
	hitTracker.TrackUpload(cDigest).CloseWithBytesTransferred(3000, 4000, repb.Compressor_IDENTITY, "test")

	expectation := hitTrackerService.casDownloadExpectation(anonKey, 1)
	require.Eventually(t, expectation, 10*time.Second, 100*time.Millisecond)
	require.Equal(t, int64(2000), hitTrackerService.casBytesDownloaded[anonKey].Load())
	require.Equal(t, int64(1), hitTrackerService.casUploads[anonKey].Load(), "Expected 1 HitTracker.Track upload")
	require.Equal(t, int64(4000), hitTrackerService.casBytesUploaded[anonKey].Load())
}

func TestCASHitTracker_NoDeduplication(t *testing.T) {
	_, hitTrackerFactory, hitTrackerService := setup(t)
	ctx := context.Background()
	hitTracker := hitTrackerFactory.NewCASHitTracker(ctx, &repb.RequestMetadata{})
	hitTracker.TrackDownload(aDigest).CloseWithBytesTransferred(1000, 2000, repb.Compressor_IDENTITY, "test")
	hitTracker.TrackDownload(aDigest).CloseWithBytesTransferred(1000, 2000, repb.Compressor_IDENTITY, "test")

	expectation := hitTrackerService.casDownloadExpectation(anonKey, 2)
	require.Eventually(t, expectation, 10*time.Second, 100*time.Millisecond)
	require.Equal(t, int64(4000), hitTrackerService.casBytesDownloaded[anonKey].Load())
}

func TestCASHitTracker_SplitsUpdates(t *testing.T) {
	flags.Set(t, "cache_proxy.remote_hit_tracker.max_hits_per_update", 10)
	authenticator, hitTrackerFactory, hitTrackerService := setup(t)

	// Pause the hit-tracker RPC service and send an RPC that'll block the
	// hit-tracker-client worker so updates are queued.
	resume := hitTrackerService.pause(t)
	group1Ctx := authenticatedContext(user1, authenticator)
	group1Tracker := hitTrackerFactory.NewCASHitTracker(group1Ctx, &repb.RequestMetadata{})
	group1Tracker.TrackDownload(fDigest).CloseWithBytesTransferred(1_000_000, 2_000_000, repb.Compressor_IDENTITY, "test")

	group1InternalCtx := authenticatedContextWithOrigin(user1, authenticator, "internal")
	internalHitTracker := hitTrackerFactory.NewCASHitTracker(group1InternalCtx, &repb.RequestMetadata{})
	internalHitTracker.TrackDownload(fDigest).CloseWithBytesTransferred(1, 2, repb.Compressor_IDENTITY, "test")
	internalHitTracker.TrackDownload(fDigest).CloseWithBytesTransferred(1_000_000, 2_000_000, repb.Compressor_IDENTITY, "test")

	anonCtx := context.Background()
	anonTracker := hitTrackerFactory.NewCASHitTracker(anonCtx, &repb.RequestMetadata{})

	for range 10 {
		anonTracker.TrackDownload(aDigest).CloseWithBytesTransferred(1, 2, repb.Compressor_IDENTITY, "test")
		anonTracker.TrackDownload(bDigest).CloseWithBytesTransferred(10, 20, repb.Compressor_IDENTITY, "test")
		anonTracker.TrackDownload(cDigest).CloseWithBytesTransferred(100, 200, repb.Compressor_IDENTITY, "test")
		anonTracker.TrackDownload(dDigest).CloseWithBytesTransferred(1_000, 2_000, repb.Compressor_IDENTITY, "test")
		anonTracker.TrackDownload(eDigest).CloseWithBytesTransferred(10_000, 20_000, repb.Compressor_IDENTITY, "test")
	}

	anotherGroup1Tracker := hitTrackerFactory.NewCASHitTracker(group1Ctx, &repb.RequestMetadata{})
	anotherGroup1Tracker.TrackDownload(fDigest).CloseWithBytesTransferred(1_000_000, 2_000_000, repb.Compressor_IDENTITY, "test")

	group1BazelCtx := authenticatedContextWithInternalBazelClient(user1, authenticator)
	group1WithBazelClientTracker := hitTrackerFactory.NewCASHitTracker(group1BazelCtx, &repb.RequestMetadata{})
	group1WithBazelClientTracker.TrackDownload(fDigest).CloseWithBytesTransferred(1_000_000, 2_030_000, repb.Compressor_IDENTITY, "test")
	resume()

	// Expect 10x [A, B, C, D, E] for ANON.
	expectation := hitTrackerService.casDownloadExpectation(anonKey, 50)
	require.Eventually(t, expectation, 10*time.Second, 100*time.Millisecond, "Expected 50 updates for group ANON")
	require.Equal(t, int64(222_220), hitTrackerService.casBytesDownloaded[anonKey].Load())

	// Group 1's update shouldn't be affected by ANON's batching
	expectation = hitTrackerService.casDownloadExpectation(group1Key, 2)
	require.Eventually(t, expectation, 10*time.Second, 100*time.Millisecond, "Expected 2 external cache hits for group 1")
	require.Equal(t, int64(4_000_000), hitTrackerService.casBytesDownloaded[group1Key].Load())

	// Group 1's update that claims to be 'internal' should be separate from its non-internal update.
	expectation = hitTrackerService.casDownloadExpectation(group1InternalKey, 2)
	require.Eventually(t, expectation, 10*time.Second, 100*time.Millisecond, "Expected 2 internal cache hits for group 1")
	require.Equal(t, int64(2_000_002), hitTrackerService.casBytesDownloaded[group1InternalKey].Load())

	// Same for Group 1's update that claims to be 'bazel' and 'internal'.
	expectation = hitTrackerService.casDownloadExpectation(group1InternalBazelKey, 1)
	require.Eventually(t, expectation, 10*time.Second, 100*time.Millisecond, "Expected 1 internal bazel cache hit for group 1")
	require.Equal(t, int64(2_030_000), hitTrackerService.casBytesDownloaded[group1InternalBazelKey].Load())
}

func TestCASHitTracker_DropsUpdates(t *testing.T) {
	flags.Set(t, "cache_proxy.remote_hit_tracker.max_hits_per_update", 10)
	flags.Set(t, "cache_proxy.remote_hit_tracker.max_pending_hits_per_key", 10)
	authenticator, hitTrackerFactory, hitTrackerService := setup(t)

	// Pause the hit-tracker RPC service and send an RPC that'll block the
	// hit-tracker-client worker so updates are queued.
	resume := hitTrackerService.pause(t)
	group1Ctx := authenticatedContext(user1, authenticator)
	hitTracker := hitTrackerFactory.NewCASHitTracker(group1Ctx, &repb.RequestMetadata{})
	hitTracker.TrackDownload(fDigest).CloseWithBytesTransferred(1_000_000, 2_000_000, repb.Compressor_IDENTITY, "test")

	anonCtx := context.Background()
	hitTracker = hitTrackerFactory.NewCASHitTracker(anonCtx, &repb.RequestMetadata{})

	for range 10 {
		hitTracker.TrackDownload(aDigest).CloseWithBytesTransferred(1, 2, repb.Compressor_IDENTITY, "test")
		hitTracker.TrackDownload(bDigest).CloseWithBytesTransferred(10, 20, repb.Compressor_IDENTITY, "test")
		hitTracker.TrackDownload(cDigest).CloseWithBytesTransferred(100, 200, repb.Compressor_IDENTITY, "test")
		hitTracker.TrackDownload(dDigest).CloseWithBytesTransferred(1_000, 2_000, repb.Compressor_IDENTITY, "test")
		hitTracker.TrackDownload(eDigest).CloseWithBytesTransferred(10_000, 20_000, repb.Compressor_IDENTITY, "test")
	}

	hitTracker = hitTrackerFactory.NewCASHitTracker(group1Ctx, &repb.RequestMetadata{})
	hitTracker.TrackDownload(fDigest).CloseWithBytesTransferred(1_000_000, 2_000_000, repb.Compressor_IDENTITY, "test")
	resume()

	// Expect A, B, C, D, E, A, B, C, D, E to be sent for ANON.
	expectation := hitTrackerService.casDownloadExpectation(anonKey, 10)
	require.Eventually(t, expectation, 10*time.Second, 100*time.Millisecond, "Expected 10 updates for group ANON")
	require.Equal(t, int64(44_444), hitTrackerService.casBytesDownloaded[anonKey].Load())

	// Even though group 1's second update came at the end, it should still be sent.
	expectation = hitTrackerService.casDownloadExpectation(group1Key, 2)
	require.Eventually(t, expectation, 10*time.Second, 100*time.Millisecond, "Expected 2 cache hits for group 1")
	require.Equal(t, int64(4_000_000), hitTrackerService.casBytesDownloaded[group1Key].Load())
}

// pendingHitsExpectation returns a function reporting whether exactly
// expected hits are batched and waiting to be sent for the given collection.
func pendingHitsExpectation(f *HitTrackerFactory, key string, expected int) func() bool {
	return func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		c, ok := f.hitsByCollection[key]
		if !ok {
			return expected == 0
		}
		return len(c.hits) == expected
	}
}

func TestCASHitTracker_AcceptsHitsWhileSplitOverflowIsPending(t *testing.T) {
	flags.Set(t, "cache_proxy.remote_hit_tracker.max_hits_per_update", 10)
	flags.Set(t, "cache_proxy.remote_hit_tracker.max_pending_hits_per_key", 100)
	clock := clockwork.NewFakeClock()
	authenticator, hitTrackerFactory, hitTrackerService := setupWithClock(t, clock)

	anonTracker := hitTrackerFactory.NewCASHitTracker(context.Background(), &repb.RequestMetadata{})
	for range 25 {
		anonTracker.TrackDownload(aDigest).CloseWithBytesTransferred(1, 2, repb.Compressor_IDENTITY, "test")
	}
	require.Eventually(t, pendingHitsExpectation(hitTrackerFactory, anonKey, 25), 10*time.Second, 10*time.Millisecond)

	// Block Track RPCs and fire the sender once: it sends the first 10 hits
	// and re-queues the other 15, which stay pending while that RPC is blocked.
	resume := hitTrackerService.pause(t)
	blockCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, clock.BlockUntilContext(blockCtx, 1), "sender never started waiting on its ticker")
	clock.Advance(*remoteHitTrackerPollInterval)
	require.Eventually(t, pendingHitsExpectation(hitTrackerFactory, anonKey, 15), 10*time.Second, 10*time.Millisecond)

	// Hits that arrive while the re-queued overflow is pending must be batched
	// onto it rather than dropped.
	for range 5 {
		anonTracker.TrackDownload(bDigest).CloseWithBytesTransferred(1, 2, repb.Compressor_IDENTITY, "test")
	}
	// The batcher handles hits in order, so once this later hit for another
	// collection is pending, the five above have been handled too.
	group1Tracker := hitTrackerFactory.NewCASHitTracker(authenticatedContext(user1, authenticator), &repb.RequestMetadata{})
	group1Tracker.TrackDownload(fDigest).CloseWithBytesTransferred(1, 2, repb.Compressor_IDENTITY, "test")
	require.Eventually(t, pendingHitsExpectation(hitTrackerFactory, group1Key, 1), 10*time.Second, 10*time.Millisecond)
	require.True(t, pendingHitsExpectation(hitTrackerFactory, anonKey, 20)(), "hits enqueued while the split overflow was pending were dropped")

	resume()
	require.Eventually(t, hitTrackerService.casDownloadExpectation(anonKey, 30), 10*time.Second, 100*time.Millisecond, "Expected 30 updates for group ANON")
	require.Eventually(t, hitTrackerService.casDownloadExpectation(group1Key, 1), 10*time.Second, 100*time.Millisecond, "Expected 1 update for group 1")
}

// TestBatchAndSendAreSerialized exists for the race detector, which CI runs.
// The batcher appends to a cacheHits while senders pop and read it, and both
// must happen under HitTrackerFactory.mu. There is no per-cacheHits lock, so
// if the append ever moves outside h.mu again, the detector reports the
// batcher's write racing the sender's read here, even on runs where no hit is
// actually lost.
func TestBatchAndSendAreSerialized(t *testing.T) {
	clock := clockwork.NewFakeClock()
	_, hitTrackerFactory, hitTrackerService := setupWithClock(t, clock)
	ctx := context.Background()
	anonTracker := hitTrackerFactory.NewCASHitTracker(ctx, &repb.RequestMetadata{})

	// The sender never ticks on the fake clock; send from here instead so that
	// sends interleave with the batcher goroutine as tightly as possible.
	const hits = 200
	for range hits {
		anonTracker.TrackDownload(aDigest).CloseWithBytesTransferred(1, 2, repb.Compressor_IDENTITY, "test")
		hitTrackerFactory.sendTrackRequest(ctx)
	}
	require.Eventually(t, func() bool {
		hitTrackerFactory.sendTrackRequest(ctx)
		return hitTrackerService.casDownloads[anonKey].Load() == hits
	}, 10*time.Second, 10*time.Millisecond, "Expected every hit to be sent exactly once")
}

func BenchmarkEnqueue(b *testing.B) {
	*log.LogLevel = "error"
	log.Configure()

	numToEnqueue := 1_000_000
	flags.Set(b, "cache_proxy.remote_hit_tracker.max_hits_per_update", b.N*numToEnqueue)
	flags.Set(b, "cache_proxy.remote_hit_tracker.max_pending_hits_per_key", b.N*numToEnqueue)

	b.ReportAllocs()
	for b.Loop() {
		// The client discards hits and logs stuff if too many hits are
		// enqueued. Recreate for each benchmark to prevent this.
		b.StopTimer()
		_, hitTrackerFactory, _ := setup(b)
		b.StartTimer()
		hitTracker := hitTrackerFactory.NewCASHitTracker(b.Context(), &repb.RequestMetadata{})
		wg := sync.WaitGroup{}
		for range numToEnqueue {
			wg.Go(func() {
				hitTracker.TrackDownload(aDigest).CloseWithBytesTransferred(1, 2, repb.Compressor_IDENTITY, "test")
			})
		}
		wg.Wait()
	}
}

func TestRegister(t *testing.T) {
	for _, tc := range []struct {
		name       string
		target     string
		workers    int
		expectNoOp bool
	}{
		{name: "no remote cache disables hit tracking", target: "", workers: 1, expectNoOp: true},
		{name: "zero workers disables hit tracking", target: "grpc://localhost:1985", workers: 0, expectNoOp: true},
		{name: "remote cache and workers enable hit tracking", target: "grpc://localhost:1985", workers: 1, expectNoOp: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags.Set(t, "cache_proxy.remote_cache", tc.target)
			flags.Set(t, "cache_proxy.remote_hit_tracker.workers", tc.workers)
			te := testenv.GetTestEnv(t)
			// Register hooks the factory's shutdown into the health checker,
			// which the test env shuts down at cleanup.
			require.NoError(t, Register(te))
			_, isNoOp := te.GetHitTrackerFactory().(*NoOpHitTrackerFactory)
			require.Equal(t, tc.expectNoOp, isNoOp)
		})
	}
}

// trackObservingConn reports the outcome of every RPC sent on the wrapped
// connection, so that tests can wait for a sender's Track RPC to finish.
type trackObservingConn struct {
	grpc.ClientConnInterface
	results chan error
}

func (c *trackObservingConn) Invoke(ctx context.Context, method string, args any, reply any, opts ...grpc.CallOption) error {
	err := c.ClientConnInterface.Invoke(ctx, method, args, reply, opts...)
	select {
	case c.results <- err:
	default:
	}
	return err
}

// harness runs the client against the fake HitTrackerService served on an
// in-memory listener. The server can be stopped and replaced by a new one,
// like an app replica going away during a rolling restart, and the client
// reconnects to whichever server is current.
type harness struct {
	te            *real_environment.RealEnv
	clock         *clockwork.FakeClock
	authenticator interfaces.Authenticator
	service       *testHitTracker
	conn          *grpc.ClientConn
	results       chan error
	factory       *HitTrackerFactory

	lis atomic.Pointer[bufconn.Listener]
	srv *grpc.Server
}

func newHarness(t *testing.T) *harness {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &harness{
		te:      testenv.GetTestEnv(t),
		clock:   clockwork.NewFakeClock(),
		results: make(chan error, 100),
	}
	// The sender only runs when the test advances the fake clock.
	h.te.SetClock(h.clock)
	h.authenticator = testauth.NewTestAuthenticator(t, testauth.TestUsers(user1, group1))
	h.te.SetAuthenticator(h.authenticator)
	h.service = newTestHitTracker(t, h.authenticator)
	h.startServer(t)
	conn, err := testenv.LocalGRPCConn(t, ctx, h.lis.Load(), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return h.lis.Load().DialContext(ctx)
	}))
	require.NoError(t, err)
	h.conn = conn
	h.factory = newHitTrackerClient(ctx, h.te, &trackObservingConn{ClientConnInterface: conn, results: h.results})
	return h
}

func (h *harness) startServer(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	srv, run := testenv.GRPCServer(h.te, lis)
	hitpb.RegisterHitTrackerServiceServer(srv, h.service)
	go run()
	t.Cleanup(srv.Stop)
	h.srv = srv
	h.lis.Store(lis)
}

// tick fires the sender once.
func (h *harness) tick(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, h.clock.BlockUntilContext(ctx, 1), "sender never started waiting on its ticker")
	h.clock.Advance(*remoteHitTrackerPollInterval)
}

// nextTrackResult waits for the client's next Track RPC to finish and
// returns its error.
func (h *harness) nextTrackResult(t *testing.T) error {
	select {
	case err := <-h.results:
		return err
	case <-time.After(10 * time.Second):
		require.FailNow(t, "timed out waiting for a Track RPC")
		return nil
	}
}

// shutdown runs the server's graceful shutdown, as on SIGTERM, and waits for
// it to finish.
func (h *harness) shutdown() {
	h.te.GetHealthChecker().Shutdown()
	h.te.GetHealthChecker().WaitForGracefulShutdown()
}

func (h *harness) trackDownloads(ctx context.Context, n int) {
	tracker := h.factory.NewCASHitTracker(ctx, &repb.RequestMetadata{})
	for range n {
		tracker.TrackDownload(aDigest).CloseWithBytesTransferred(1, 2, repb.Compressor_IDENTITY, "test")
	}
}

// A proxy can be asked to shut down while the batcher is behind, e.g. right
// after a burst of downloads. Hits waiting in the enqueue channel at that
// point were served to clients and must still be reported.
func TestShutdownReportsHitsStillInEnqueueChannel(t *testing.T) {
	h := newHarness(t)

	// Stall the batcher on the factory lock so that hits pile up in the
	// enqueue channel.
	h.factory.mu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(h.factory.mu.Unlock) }
	t.Cleanup(unlock)
	const hits = 100
	h.trackDownloads(context.Background(), hits)

	go h.shutdown()
	require.Eventually(t, h.factory.shouldFlushSynchronously, 10*time.Second, time.Millisecond, "shutdown never started")
	unlock()
	h.shutdown()

	require.Equal(t, int64(hits), h.service.casDownloads[anonKey].Load(), "hits reported to the app after shutdown")
}

// Same as above, but without stalling anything: many concurrent downloads
// finish right before the proxy is told to shut down.
func TestShutdownRightAfterBurstReportsEveryHit(t *testing.T) {
	h := newHarness(t)

	const goroutines, hitsPerGoroutine = 8, 10_000
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() { h.trackDownloads(context.Background(), hitsPerGoroutine) })
	}
	wg.Wait()
	h.shutdown()

	require.Equal(t, int64(goroutines*hitsPerGoroutine), h.service.casDownloads[anonKey].Load(), "hits reported to the app after shutdown")
}

func TestTrackUnavailableIsRetriedOnNextTick(t *testing.T) {
	h := newHarness(t)
	h.trackDownloads(context.Background(), 10)
	require.Eventually(t, pendingHitsExpectation(h.factory, anonKey, 10), 10*time.Second, 10*time.Millisecond)

	h.service.failNextTracks(status.UnavailableError("injected"))
	h.tick(t)
	require.True(t, status.IsUnavailableError(h.nextTrackResult(t)))

	h.tick(t)
	require.Eventually(t, h.service.casDownloadExpectation(anonKey, 10), 5*time.Second, 10*time.Millisecond,
		"hits reported after one Unavailable: got %d, want 10", h.service.casDownloads[anonKey].Load())
}

// Simulates an app replica that goes away between two flushes, as in a
// rolling restart: the proxy's next Track RPC fails with a real Unavailable
// from gRPC, and the following one reaches the replacement replica.
func TestAppReplicaRestartDoesNotLoseHits(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.trackDownloads(ctx, 10)
	require.Eventually(t, pendingHitsExpectation(h.factory, anonKey, 10), 10*time.Second, 10*time.Millisecond)
	h.tick(t)
	require.NoError(t, h.nextTrackResult(t))
	require.Equal(t, int64(10), h.service.casDownloads[anonKey].Load())

	// The replica goes away. Downloads keep being served from the proxy.
	h.srv.Stop()
	h.trackDownloads(ctx, 7)
	require.Eventually(t, pendingHitsExpectation(h.factory, anonKey, 7), 10*time.Second, 10*time.Millisecond)
	h.tick(t)
	err := h.nextTrackResult(t)
	require.True(t, status.IsUnavailableError(err), "expected Unavailable while the app is down, got %v", err)

	// A replacement replica comes up and the connection recovers.
	h.startServer(t)
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	h.conn.Connect()
	for state := h.conn.GetState(); state != connectivity.Ready; state = h.conn.GetState() {
		require.True(t, h.conn.WaitForStateChange(readyCtx, state), "connection never became ready")
	}

	h.tick(t)
	require.Eventually(t, h.service.casDownloadExpectation(anonKey, 17), 5*time.Second, 10*time.Millisecond,
		"hits reported across the restart: got %d, want 17", h.service.casDownloads[anonKey].Load())
}

// Hits put back after a failed send count against the same per-collection
// limit as new hits, so an app outage cannot grow the proxy's memory beyond
// max_pending_hits_per_key.
func TestRetriedHitsRespectMaxPendingHitsPerKey(t *testing.T) {
	flags.Set(t, "cache_proxy.remote_hit_tracker.max_pending_hits_per_key", 10)
	h := newHarness(t)
	ctx := context.Background()

	h.trackDownloads(ctx, 8)
	require.Eventually(t, pendingHitsExpectation(h.factory, anonKey, 8), 10*time.Second, 10*time.Millisecond)
	h.service.failNextTracks(status.UnavailableError("injected"))
	// Send from the test instead of ticking the sender, so that this flush
	// is over before the hits below arrive. A flush that is still running
	// would send them right away.
	h.factory.sendTrackRequest(ctx)
	require.True(t, status.IsUnavailableError(h.nextTrackResult(t)))

	h.trackDownloads(ctx, 5)
	// The batcher handles hits in order, so once this later hit for another
	// collection is pending, the five above have been handled too.
	h.trackDownloads(authenticatedContext(user1, h.authenticator), 1)
	require.Eventually(t, pendingHitsExpectation(h.factory, group1Key, 1), 10*time.Second, 10*time.Millisecond)
	h.factory.mu.Lock()
	pending := len(h.factory.hitsByCollection[anonKey].hits)
	h.factory.mu.Unlock()
	require.LessOrEqual(t, pending, 10)

	h.tick(t)
	require.Eventually(t, h.service.casDownloadExpectation(group1Key, 1), 5*time.Second, 10*time.Millisecond)
	require.Equal(t, int64(10), h.service.casDownloads[anonKey].Load(), "8 retried + 5 new hits, capped at 10")
}

// The fake service records nothing when it returns an error, but the real
// one records hits one by one, so after most errors the app may already have
// counted some or all of the batch. Those batches must not be resent.
func TestTrackNotRetriedWhenAppMayHaveRecordedHits(t *testing.T) {
	for _, err := range []error{
		status.DeadlineExceededError("injected"),
		status.InternalError("injected"),
		status.InvalidArgumentError("injected"),
		status.ResourceExhaustedError("injected"),
	} {
		t.Run(err.Error(), func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			h.trackDownloads(ctx, 10)
			require.Eventually(t, pendingHitsExpectation(h.factory, anonKey, 10), 10*time.Second, 10*time.Millisecond)
			h.service.failNextTracks(err)
			h.tick(t)
			require.Error(t, h.nextTrackResult(t))

			// The sender may still be flushing, so it may send this hit
			// without another tick. A resent batch would be sent no later
			// than this hit.
			h.trackDownloads(ctx, 1)
			require.Eventually(t, func() bool {
				if h.service.casDownloads[anonKey].Load() > 0 {
					return true
				}
				h.clock.Advance(*remoteHitTrackerPollInterval)
				return false
			}, 10*time.Second, 10*time.Millisecond)
			require.Equal(t, int64(1), h.service.casDownloads[anonKey].Load())
		})
	}
}

// If the connection to the app breaks after the app has recorded a batch but
// before the response reaches the proxy, the proxy sees Unavailable and
// resends the batch, so the app counts its hits twice. This is the accepted
// trade-off of retrying Unavailable (see canRetry): the batch is recorded
// once more, not again on later ticks.
func TestUnavailableAfterAppRecordedBatchIsCountedOnceMore(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.trackDownloads(ctx, 10)
	require.Eventually(t, pendingHitsExpectation(h.factory, anonKey, 10), 10*time.Second, 10*time.Millisecond)

	recorded := make(chan struct{})
	release := make(chan struct{})
	var recordedOnce, releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFn)
	hook := func() {
		recordedOnce.Do(func() {
			close(recorded)
			<-release
		})
	}
	h.service.afterRecord.Store(&hook)

	h.tick(t)
	select {
	case <-recorded:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "Track never reached the app")
	}
	require.Equal(t, int64(10), h.service.casDownloads[anonKey].Load())
	// The app goes away while its handler is still running.
	h.srv.Stop()
	err := h.nextTrackResult(t)
	releaseFn()
	t.Logf("Track error seen by the proxy: %v", err)
	require.True(t, status.IsUnavailableError(err), "expected Unavailable, got %v", err)

	h.startServer(t)
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	h.conn.Connect()
	for state := h.conn.GetState(); state != connectivity.Ready; state = h.conn.GetState() {
		require.True(t, h.conn.WaitForStateChange(readyCtx, state), "connection never became ready")
	}

	// A hit for another collection, to check that the connection works.
	h.trackDownloads(authenticatedContext(user1, h.authenticator), 1)
	require.Eventually(t, pendingHitsExpectation(h.factory, group1Key, 1), 10*time.Second, 10*time.Millisecond)
	h.tick(t)
	require.Eventually(t, h.service.casDownloadExpectation(group1Key, 1), 10*time.Second, 10*time.Millisecond)
	// The held batch is put back on the queue after the group1 hit.
	require.Eventually(t, h.service.casDownloadExpectation(anonKey, 20), 10*time.Second, 10*time.Millisecond,
		"hits recorded by the app: %d", h.service.casDownloads[anonKey].Load())

	// Nothing is left to resend.
	pending, held := retainedHits(h.factory)
	require.Equal(t, 0, pending+held)
	h.trackDownloads(authenticatedContext(user1, h.authenticator), 1)
	require.Eventually(t, func() bool {
		if h.service.casDownloads[group1Key].Load() == 2 {
			return true
		}
		h.clock.Advance(*remoteHitTrackerPollInterval)
		return false
	}, 10*time.Second, 10*time.Millisecond)
	require.Equal(t, int64(20), h.service.casDownloads[anonKey].Load(), "hits recorded by the app")
}

// Shutdown should finish within max_shutdown_duration even when the app
// never answers a Track RPC.
func TestVerifyShutdownReturnsWhenTrackHangs(t *testing.T) {
	flags.Set(t, "max_shutdown_duration", 2*time.Second)
	h := newHarness(t)
	ctx := context.Background()
	h.trackDownloads(ctx, 10)
	require.Eventually(t, pendingHitsExpectation(h.factory, anonKey, 10), 10*time.Second, 10*time.Millisecond)
	resume := h.service.pause(t)
	h.tick(t)
	// Wait for the sender to take the batch, i.e. to be inside Track.
	require.Eventually(t, func() bool {
		h.factory.mu.Lock()
		defer h.factory.mu.Unlock()
		return len(h.factory.hitsQueue) == 0
	}, 10*time.Second, 10*time.Millisecond)

	done := make(chan struct{})
	start := time.Now()
	go func() {
		h.shutdown()
		close(done)
	}()
	select {
	case <-done:
		t.Logf("shutdown returned after %s", time.Since(start))
	case <-time.After(10 * time.Second):
		resume()
		<-done
		require.FailNow(t, "shutdown still blocked 10s after it started (max_shutdown_duration 2s) while a Track RPC hung")
	}
}

// retainedHits returns the number of hits that are batched and waiting to be
// sent, and the number held for retry after a failed Track RPC.
func retainedHits(f *HitTrackerFactory) (pending, held int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.hitsByCollection {
		pending += len(c.hits)
	}
	return pending, f.failedHits
}

// During a long app outage, every batch is still tried once per tick and the
// hits that are kept for retry stay within max_retry_hits, however long the
// outage lasts. The rest are dropped and counted.
func TestLongOutageKeepsRetainedHitsBounded(t *testing.T) {
	const maxRetryHits, maxAttempts = 25, 3
	flags.Set(t, "cache_proxy.remote_hit_tracker.max_hits_per_update", 10)
	flags.Set(t, "cache_proxy.remote_hit_tracker.max_retry_hits", maxRetryHits)
	flags.Set(t, "cache_proxy.remote_hit_tracker.max_track_attempts", maxAttempts)
	h := newHarness(t)
	ctxs := []context.Context{
		context.Background(),
		authenticatedContext(user1, h.authenticator),
		authenticatedContextWithOrigin(user1, h.authenticator, "internal"),
	}
	keys := []string{anonKey, group1Key, group1InternalKey}
	dropped := func() int {
		n := 0.0
		for _, g := range []string{interfaces.AuthAnonymousUser, group1} {
			for _, s := range []string{"dropped_send_failed", "dropped_too_many_updates"} {
				n += testutil.ToFloat64(metrics.RemoteHitTrackerUpdates.WithLabelValues(g, s))
			}
		}
		return int(n)
	}
	droppedBefore := dropped()
	delivered := func() int {
		n := int64(0)
		for _, k := range keys {
			n += h.service.casDownloads[k].Load()
		}
		return int(n)
	}
	// accountedFor reports whether every tracked hit has been batched and is
	// not in a Track RPC, i.e. is pending, held, delivered or dropped.
	issued := 0
	accountedFor := func() bool {
		pending, held := retainedHits(h.factory)
		return pending+held+delivered()+dropped()-droppedBefore == issued
	}

	h.srv.Stop()
	const rounds, hitsPerKey = 40, 15
	for i := range rounds {
		for _, ctx := range ctxs {
			h.trackDownloads(ctx, hitsPerKey)
			issued += hitsPerKey
		}
		require.Eventually(t, accountedFor, 10*time.Second, time.Millisecond)
		h.tick(t)
		require.Eventually(t, func() bool {
			pending, _ := retainedHits(h.factory)
			return pending == 0 && accountedFor()
		}, 10*time.Second, time.Millisecond, "round %d: flush did not try every batch", i)
		_, held := retainedHits(h.factory)
		require.LessOrEqual(t, held, maxRetryHits, "round %d", i)
	}
	require.Equal(t, 0, delivered())
	t.Logf("%d hits tracked during the outage, %d dropped", issued, dropped()-droppedBefore)

	// The app comes back. Only the hits still held for retry are delivered.
	_, held := retainedHits(h.factory)
	require.Greater(t, held, 0)
	h.startServer(t)
	readyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h.conn.Connect()
	for state := h.conn.GetState(); state != connectivity.Ready; state = h.conn.GetState() {
		require.True(t, h.conn.WaitForStateChange(readyCtx, state), "connection never became ready")
	}
	h.tick(t)
	require.Eventually(t, func() bool {
		pending, held := retainedHits(h.factory)
		return pending == 0 && held == 0 && accountedFor()
	}, 10*time.Second, time.Millisecond)
	require.Equal(t, held, delivered())
}

// A Track RPC that the app never answers times out instead of stalling the
// sender. The app may have recorded the batch, so it is not resent.
func TestHungTrackTimesOut(t *testing.T) {
	flags.Set(t, "cache_proxy.remote_hit_tracker.track_timeout", 200*time.Millisecond)
	h := newHarness(t)
	ctx := context.Background()
	h.trackDownloads(ctx, 10)
	require.Eventually(t, pendingHitsExpectation(h.factory, anonKey, 10), 10*time.Second, 10*time.Millisecond)
	resume := h.service.pause(t)
	h.tick(t)
	err := h.nextTrackResult(t)
	require.True(t, status.IsDeadlineExceededError(err), "expected DeadlineExceeded, got %v", err)
	resume()

	// The sender is free again: a later hit is reported on the next tick.
	h.trackDownloads(authenticatedContext(user1, h.authenticator), 1)
	require.Eventually(t, func() bool {
		if h.service.casDownloads[group1Key].Load() == 1 {
			return true
		}
		h.clock.Advance(*remoteHitTrackerPollInterval)
		return false
	}, 10*time.Second, 10*time.Millisecond)
	_, held := retainedHits(h.factory)
	require.Equal(t, 0, held)
}
