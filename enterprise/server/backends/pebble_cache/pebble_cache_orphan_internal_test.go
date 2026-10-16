package pebble_cache

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/filestore"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testdigest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/util/lockmap"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"

	rspb "github.com/buildbuddy-io/buildbuddy/proto/resource"
	sgpb "github.com/buildbuddy-io/buildbuddy/proto/storage"
)

// hookLocker wraps a Locker and lets a test run code at precise points in
// the lock protocol for one specific lock ID. Each hook fires at most once.
type hookLocker struct {
	lockmap.Locker[string]

	mu          sync.Mutex
	lockID      string
	beforeLock  func() // runs before Lock(lockID) acquires the lock
	afterRUnlck func() // runs right after an RLock(lockID) is released
}

func (h *hookLocker) take(f *func()) func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn := *f
	*f = nil
	return fn
}

func (h *hookLocker) Lock(key string) func() {
	if key == h.lockID {
		if fn := h.take(&h.beforeLock); fn != nil {
			fn()
		}
	}
	return h.Locker.Lock(key)
}

func (h *hookLocker) RLock(key string) func() {
	unlock := h.Locker.RLock(key)
	if key != h.lockID {
		return unlock
	}
	return func() {
		unlock()
		if fn := h.take(&h.afterRUnlck); fn != nil {
			fn()
		}
	}
}

func newOrphanTestCache(t *testing.T) (*PebbleCache, context.Context) {
	flags.Set(t, "cache.pebble.orphan_delete_dry_run", false)
	te := testenv.GetTestEnv(t)
	te.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers()))
	ctx, err := prefix.AttachUserPrefixToContext(context.Background(), te.GetAuthenticator())
	require.NoError(t, err)
	pc, err := NewPebbleCache(te, &Options{RootDirectory: testfs.MakeTempDir(t), MaxSizeBytes: 1_000_000_000})
	require.NoError(t, err)
	require.NoError(t, pc.Start())
	t.Cleanup(func() { _ = pc.Stop() })
	return pc, ctx
}

func orphanTestKey(t *testing.T, pc *PebbleCache, ctx context.Context, r *rspb.ResourceName) filestore.PebbleKey {
	r, _ = pc.autoZstdResource(r)
	fr, err := pc.makeFileRecord(pc.userGroupID(ctx), nil, r)
	require.NoError(t, err)
	key, err := pc.fileStorer.PebbleKey(fr)
	require.NoError(t, err)
	return key
}

// requireMetadataPointsToExistingFile asserts the invariant under test:
// if metadata for key is committed and refers to a file on local disk,
// that file exists.
func requireMetadataPointsToExistingFile(t *testing.T, pc *PebbleCache, ctx context.Context, key filestore.PebbleKey, r *rspb.ResourceName) {
	db, err := pc.leaser.DB()
	require.NoError(t, err)
	defer db.Close()
	md := &sgpb.FileMetadata{}
	require.NoError(t, pc.lookupFileMetadata(ctx, db, key, md), "metadata should be committed after a successful write")
	fmd := md.GetStorageMetadata().GetFileMetadata()
	require.NotNil(t, fmd, "expected blob to be stored as a local file")
	fp := pc.fileStorer.FilePath(pc.blobDirectory, fmd)

	contains, err := pc.Contains(ctx, r)
	require.NoError(t, err)
	_, statErr := os.Stat(fp)
	_, getErr := pc.Get(ctx, r)
	t.Logf("after cleanup: Contains=%v, stat(file) err=%v, Get err=%v", contains, statErr, getErr)
	require.NoError(t, statErr, "committed metadata points to a file that orphan cleanup deleted")
	require.NoError(t, getErr)
}

// A blob file is left on disk without metadata (e.g. after a crash or a
// metadata-only delete). Orphan cleanup decides it is an orphan, and before
// it unlinks the file, a client writes the same blob again (renaming a new
// file over the same path and committing metadata).
func TestOrphanCleanupRacesWithRewriteOfOrphanedBlob(t *testing.T) {
	pc, ctx := newOrphanTestCache(t)
	r, buf := testdigest.NewRandomResourceAndBuf(t, 10000, rspb.CacheType_CAS, "")
	key := orphanTestKey(t, pc, ctx, r)

	require.NoError(t, pc.Set(ctx, r, buf))
	unlock := pc.locker.Lock(key.LockID())
	require.NoError(t, pc.deleteMetadataOnly(ctx, key))
	unlock()

	hl := &hookLocker{Locker: pc.locker, lockID: key.LockID()}
	var writeErr error
	hl.afterRUnlck = func() {
		// Cleanup has looked up the metadata (not found) and released
		// its read lock; it has not yet removed the file.
		writeErr = pc.Set(ctx, r, buf)
	}
	pc.locker = hl

	require.NoError(t, pc.deleteOrphanedFiles(make(chan struct{})))
	require.Nil(t, hl.afterRUnlck, "hook should have fired")
	require.NoError(t, writeErr, "concurrent write should succeed")

	requireMetadataPointsToExistingFile(t, pc, ctx, key, r)
}

// A fresh write has published its file but not yet committed its metadata
// when orphan cleanup visits the file.
func TestOrphanCleanupRacesWithFreshWrite(t *testing.T) {
	pc, ctx := newOrphanTestCache(t)
	r, buf := testdigest.NewRandomResourceAndBuf(t, 10000, rspb.CacheType_CAS, "")
	key := orphanTestKey(t, pc, ctx, r)

	hl := &hookLocker{Locker: pc.locker, lockID: key.LockID()}
	var cleanupErr error
	hl.beforeLock = func() {
		// The writer is about to take the key lock to commit metadata.
		cleanupErr = pc.deleteOrphanedFiles(make(chan struct{}))
	}
	pc.locker = hl

	require.NoError(t, pc.Set(ctx, r, buf))
	require.Nil(t, hl.beforeLock, "hook should have fired")
	require.NoError(t, cleanupErr)

	requireMetadataPointsToExistingFile(t, pc, ctx, key, r)
}
