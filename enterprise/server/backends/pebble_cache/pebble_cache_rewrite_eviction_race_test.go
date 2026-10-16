package pebble_cache

// These tests attempt eviction after backing data publication. Local file
// commits must keep the key locked until metadata publication completes.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/filestore"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/util/approxlru"
	"github.com/buildbuddy-io/buildbuddy/server/util/lockmap"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	sgpb "github.com/buildbuddy-io/buildbuddy/proto/storage"
)

func anonCtx(t *testing.T, env *testenv.TestEnv) context.Context {
	env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers()))
	ctx, err := prefix.AttachUserPrefixToContext(context.Background(), env.GetAuthenticator())
	require.NoError(t, err)
	return ctx
}

// observedCommitLocker lets the barrier distinguish eviction that can complete
// synchronously from eviction blocked by the writer. No sleeps are needed.
type observedCommitLocker struct {
	lockmap.Locker[string]
	held      atomic.Bool
	attempted chan struct{}
	armed     atomic.Bool
	target    atomic.Value
}

func (l *observedCommitLocker) Lock(key string) func() {
	if key != l.target.Load().(string) {
		return l.Locker.Lock(key)
	}
	if l.armed.Swap(false) {
		l.attempted <- struct{}{}
	}
	unlock := l.Locker.Lock(key)
	l.held.Store(true)
	return func() { l.held.Store(false); unlock() }
}

func observeCommitLock(pc *PebbleCache) *observedCommitLocker {
	l := &observedCommitLocker{Locker: pc.locker, attempted: make(chan struct{}, 1)}
	l.target.Store("")
	pc.locker = l
	for _, e := range pc.evictors {
		e.locker = l
	}
	return l
}

// Start eviction of an old sample from the data/metadata barrier. On baseline
// it finishes inside the barrier and deletes the new file. With serialization
// it reaches Lock and waits until Commit releases the key.
func evictionAtCommitBarrier(l *observedCommitLocker, evict func()) <-chan struct{} {
	held := l.held.Load()
	l.armed.Store(true)
	done := make(chan struct{})
	go func() { evict(); close(done) }()
	<-l.attempted
	if !held {
		<-done
	}
	return done
}

func oldEntryEviction(t *testing.T, pc *PebbleCache, ctx context.Context, key filestore.PebbleKey) func() {
	db, err := pc.leaser.DB()
	require.NoError(t, err)
	defer db.Close()

	md := &sgpb.FileMetadata{}
	version, err := pc.lookupFileMetadataAndVersion(ctx, db, key, md)
	require.NoError(t, err, "old entry should still be present during the window")
	keyBytes, err := key.Bytes(version)
	require.NoError(t, err)

	sample := &approxlru.Sample[*evictionKey]{
		Key:       &evictionKey{bytes: keyBytes},
		SizeBytes: md.GetStoredSizeBytes(),
		Timestamp: time.UnixMicro(md.GetLastAccessUsec()),
	}
	require.Len(t, pc.evictors, 1)
	return func() { pc.evictors[0].doEvict(sample) }
}

// TestRewriteEvictionRace_External FAILS on baseline: it demonstrates the bug.
// After a successful rewrite, Contains() reports the blob present but Get()
// cannot read it because the interleaved eviction deleted the shared backing
// file that the rewrite had just published.
func TestRewriteEvictionRace_External(t *testing.T) {
	runRewriteEvictionRace(t, 2048 /* external: >= maxInlineFileSizeBytes */)
}

// TestRewriteEvictionRace_Inline PASSES on baseline: inline blobs store their
// bytes inside the metadata record, so the deleted-file race cannot affect
// them. This is the control showing the bug is specific to external storage.
func TestRewriteEvictionRace_Inline(t *testing.T) {
	runRewriteEvictionRace(t, 100 /* inline: < maxInlineFileSizeBytes */)
}

func runRewriteEvictionRace(t *testing.T, blobSize int64) {
	t.Cleanup(func() { beforeWriteMetadataHook = nil })

	env := testenv.GetTestEnv(t)
	ctx := anonCtx(t, env)

	clock := clockwork.NewFakeClock()
	opts := &Options{
		Clock:                  clock,
		MinEvictionAge:         new(time.Hour),
		RootDirectory:          testfs.MakeTempDir(t),
		MaxSizeBytes:           int64(1_000_000_000),
		MaxInlineFileSizeBytes: 1024,
	}
	pc, err := NewPebbleCache(env, opts)
	require.NoError(t, err)
	l := observeCommitLock(pc)
	require.NoError(t, pc.Start())
	defer pc.Stop()

	// Same content => same digest => same key and same backing file path for
	// both the initial write and the rewrite.
	t.Log("seed=20261016")
	d, buf, err := digest.UniformRandomGenerator(20261016).RandomDigestBuf(blobSize)
	require.NoError(t, err)
	r := digest.NewCASResourceName(d, "", repb.DigestFunction_SHA256).ToProto()

	// Initial write of the entry ("v1").
	require.NoError(t, pc.Set(ctx, r, buf))

	clock.Advance(2 * time.Hour)
	record, err := pc.makeFileRecord(pc.userGroupID(ctx), nil, r)
	require.NoError(t, err)
	key, err := pc.fileStorer.PebbleKey(record)
	require.NoError(t, err)
	l.target.Store(key.LockID())
	var evicted <-chan struct{}
	beforeWriteMetadataHook = func(key filestore.PebbleKey) {
		beforeWriteMetadataHook = nil
		evict := oldEntryEviction(t, pc, ctx, key)
		evicted = evictionAtCommitBarrier(l, evict)
	}
	require.NoError(t, pc.Set(ctx, r, buf))
	<-evicted

	// What a Bazel client sees first: FindMissing / Contains report the blob
	// present (they only consult metadata, never the backing file). This holds
	// for both external and inline blobs.
	contains, err := pc.Contains(ctx, r)
	require.NoError(t, err)
	require.True(t, contains, "Contains should report the digest present after the rewrite")

	// Correctness guarantee under test: a blob whose write Commit succeeded and
	// which Contains reports present must be readable. The external case
	// violates this on baseline (backing file was deleted by the interleaved
	// eviction), so Get fails. The inline case upholds it.
	got, err := pc.Get(ctx, r)
	require.NoError(t, err, "blob reported present by Contains must be readable; Get err: %v", err)
	require.Equal(t, buf, got)
}
