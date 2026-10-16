package pebble_cache

// This is an internal (white-box) test for the race described in task
// repro-pebble-eviction: a blob rewrite publishes its backing (external) file
// to the shared, key-derived path *before* it writes metadata under the
// per-key lock. If eviction of the previous entry for the same key runs in
// that window, it deletes the (now rewritten) shared file, and the rewrite
// then publishes metadata that points at a file that no longer exists.
//
// The test uses the nil-in-production beforeWriteMetadataHook barrier to run
// eviction deterministically inside that window, using the real doEvict code
// path (which takes the per-key lock, validates the old atime, then deletes
// the key and its backing file).

import (
	"context"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/filestore"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testdigest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/util/approxlru"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/stretchr/testify/require"

	sgpb "github.com/buildbuddy-io/buildbuddy/proto/storage"
)

func anonCtx(t *testing.T, env *testenv.TestEnv) context.Context {
	env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers()))
	ctx, err := prefix.AttachUserPrefixToContext(context.Background(), env.GetAuthenticator())
	require.NoError(t, err)
	return ctx
}

// evictOldEntryInWindow runs the real doEvict path against whatever metadata
// is currently stored under key (which, when called from the write-commit
// barrier, is the *old* entry for the digest being rewritten).
func evictOldEntryInWindow(t *testing.T, pc *PebbleCache, ctx context.Context, key filestore.PebbleKey) {
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
	pc.evictors[0].doEvict(sample)
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

	opts := &Options{
		RootDirectory:          testfs.MakeTempDir(t),
		MaxSizeBytes:           int64(1_000_000_000),
		MaxInlineFileSizeBytes: 1024,
	}
	pc, err := NewPebbleCache(env, opts)
	require.NoError(t, err)
	require.NoError(t, pc.Start())
	defer pc.Stop()

	// Same content => same digest => same key and same backing file path for
	// both the initial write and the rewrite.
	r, buf := testdigest.RandomCASResourceBuf(t, blobSize)

	// Initial write of the entry ("v1").
	require.NoError(t, pc.Set(ctx, r, buf))

	// Arm the barrier so the next write's commit evicts the old entry in the
	// window between publishing the backing file and writing metadata. It
	// fires once.
	beforeWriteMetadataHook = func(key filestore.PebbleKey) {
		beforeWriteMetadataHook = nil
		evictOldEntryInWindow(t, pc, ctx, key)
	}

	// Rewrite the same digest ("v2"). Commit returns success.
	require.NoError(t, pc.Set(ctx, r, buf))

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
