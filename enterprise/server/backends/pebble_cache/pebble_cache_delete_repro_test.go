package pebble_cache

import (
	"context"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/filestore"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	sgpb "github.com/buildbuddy-io/buildbuddy/proto/storage"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/stretchr/testify/require"
)

// Interpose only at the storage interface; production source is unchanged.
type deleteReproStore struct {
	filestore.Store
	afterPublish func()
}

type deleteReproWriter struct {
	interfaces.CommittedMetadataWriteCloser
	store *deleteReproStore
}

func (s *deleteReproStore) FileWriter(ctx context.Context, dir string, r *sgpb.FileRecord) (interfaces.CommittedMetadataWriteCloser, error) {
	w, err := s.Store.FileWriter(ctx, dir, r)
	if err != nil {
		return nil, err
	}
	return &deleteReproWriter{CommittedMetadataWriteCloser: w, store: s}, nil
}

func (w *deleteReproWriter) Commit() error {
	if err := w.CommittedMetadataWriteCloser.Commit(); err != nil {
		return err
	}
	if f := w.store.afterPublish; f != nil {
		f()
	}
	return nil
}

// Baseline-only reproducer: synchronously complete Delete after the replacement
// file is published but before the metadata is published. The serialization fix
// forbids this schedule (and would block this synchronous test callback).
func TestBaselineDeleteRewriteMissingFile(t *testing.T) {
	env := testenv.GetTestEnv(t)
	ctx, err := prefix.AttachUserPrefixToContext(context.Background(), env.GetAuthenticator())
	require.NoError(t, err)
	pc, err := NewPebbleCache(env, &Options{
		RootDirectory: t.TempDir(), MaxSizeBytes: 100_000_000,
		MaxInlineFileSizeBytes: 1024, MinBytesAutoZstdCompression: new(int64(1 << 60)),
	})
	require.NoError(t, err)
	store := &deleteReproStore{Store: pc.fileStorer}
	pc.fileStorer = store
	require.NoError(t, pc.Start())
	defer pc.Stop()
	t.Log("seed=20261017")
	d, data, err := digest.UniformRandomGenerator(20261017).RandomDigestBuf(4096)
	require.NoError(t, err)
	r := digest.NewCASResourceName(d, "", repb.DigestFunction_SHA256).ToProto()
	require.NoError(t, pc.Set(ctx, r, data))
	store.afterPublish = func() { require.NoError(t, pc.Delete(ctx, r)) }
	require.NoError(t, pc.Set(ctx, r, data))
	present, err := pc.Contains(ctx, r)
	require.NoError(t, err)
	require.True(t, present)
	got, err := pc.Get(ctx, r)
	require.NoError(t, err, "successful rewrite advertises a file that concurrent Delete removed")
	require.Equal(t, data, got)
}
