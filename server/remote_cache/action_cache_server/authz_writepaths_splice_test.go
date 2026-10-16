package action_cache_server_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/cachetools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/claims"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/require"

	cappb "github.com/buildbuddy-io/buildbuddy/proto/capability"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	bspb "google.golang.org/genproto/googleapis/bytestream"
)

// TestCASWriteOnlyKeyCannotWriteActionCacheViaSpliceBlob asserts a security
// invariant: a key that has CAS_WRITE but NOT CACHE_WRITE (the "developer" role
// per server/util/role/role.go: DeveloperCapabilities = {CAS_WRITE}) must not be
// able to persist an entry in the Action Cache keyspace.
//
// AC.UpdateActionResult enforces this: it requires CACHE_WRITE and for a
// CAS-write-only key it no-ops (does not persist). We verify that here as the
// baseline for the invariant.
//
// CAS.SpliceBlob, however, gates on CACHE_WRITE|CAS_WRITE ("either") and stores
// a chunked manifest as an AC entry (server/remote_cache/chunking: Manifest.store
// -> acResourceName -> CacheType_AC). So the same CAS-write-only key CAN create
// a usable AC-keyspace entry via SpliceBlob, which SplitBlob then reads back.
//
// The test encodes the invariant by asserting that, after SpliceBlob by a
// CAS-write-only key, no usable AC manifest entry exists (SplitBlob should not
// find one). On the unmodified baseline SplitBlob succeeds, so this assertion
// fails, demonstrating the asymmetry.
func TestCASWriteOnlyKeyCannotWriteActionCacheViaSpliceBlob(t *testing.T) {
	ctx := context.Background()
	te := testenv.GetTestEnv(t)
	te.SetAuthenticator(testauth.NewTestAuthenticator(t, nil))

	clientConn := runACServer(ctx, t, te)
	acClient := repb.NewActionCacheClient(clientConn)
	casClient := repb.NewContentAddressableStorageClient(clientConn)
	bsClient := bspb.NewByteStreamClient(clientConn)

	// Developer role: CAS_WRITE only, no CACHE_WRITE.
	devCtx := testauth.WithAuthenticatedUserInfo(ctx, &claims.Claims{
		UserID: "US-DEV", GroupID: "GR-DEV", AllowedGroups: []string{"GR-DEV"},
		Capabilities: []cappb.Capability{cappb.Capability_CAS_WRITE},
	})

	df := repb.DigestFunction_SHA256

	// --- Establish the invariant via the front door (AC.UpdateActionResult). ---
	// A CAS-write-only key cannot persist an AC entry: UpdateActionResult no-ops.
	payloadDigest, err := cachetools.UploadBlobToCAS(devCtx, bsClient, "", df, []byte("dev payload"))
	require.NoError(t, err, "CAS-write-only key must be able to write to the CAS")
	actionDigest := &repb.Digest{Hash: "0000000000000000000000000000000000000000000000000000000000000001", SizeBytes: 1}
	_, err = acClient.UpdateActionResult(devCtx, &repb.UpdateActionResultRequest{
		ActionDigest:   actionDigest,
		DigestFunction: df,
		ActionResult:   &repb.ActionResult{OutputFiles: []*repb.OutputFile{{Path: "f", Digest: payloadDigest}}},
	})
	require.NoError(t, err, "UpdateActionResult no-ops (does not error) for read/CAS-only keys")
	_, err = acClient.GetActionResult(devCtx, &repb.GetActionResultRequest{ActionDigest: actionDigest, DigestFunction: df})
	require.True(t, status.IsNotFoundError(err),
		"invariant: CAS-write-only key must NOT be able to persist an AC entry via UpdateActionResult; got err=%v", err)

	// --- Now the side door (CAS.SpliceBlob), which stores an AC manifest. ---
	// Build a blob out of two chunks and upload the chunks to the CAS.
	chunk1 := bytes.Repeat([]byte("A"), 100_000)
	chunk2 := bytes.Repeat([]byte("B"), 100_000)
	fullBlob := append(append([]byte{}, chunk1...), chunk2...)

	d1, err := digest.Compute(bytes.NewReader(chunk1), df)
	require.NoError(t, err)
	d2, err := digest.Compute(bytes.NewReader(chunk2), df)
	require.NoError(t, err)
	blobDigest, err := digest.Compute(bytes.NewReader(fullBlob), df)
	require.NoError(t, err)

	_, err = casClient.BatchUpdateBlobs(devCtx, &repb.BatchUpdateBlobsRequest{
		DigestFunction: df,
		Requests: []*repb.BatchUpdateBlobsRequest_Request{
			{Digest: d1, Data: chunk1},
			{Digest: d2, Data: chunk2},
		},
	})
	require.NoError(t, err, "CAS-write-only key must be able to upload chunks to the CAS")

	spliceResp, spliceErr := casClient.SpliceBlob(devCtx, &repb.SpliceBlobRequest{
		BlobDigest:     blobDigest,
		ChunkDigests:   []*repb.Digest{d1, d2},
		DigestFunction: df,
	})
	t.Logf("SpliceBlob (CAS-write-only key) err=%v resp=%v", spliceErr, spliceResp)

	// Read back the manifest from the AC via SplitBlob.
	splitResp, splitErr := casClient.SplitBlob(devCtx, &repb.SplitBlobRequest{
		BlobDigest:     blobDigest,
		DigestFunction: df,
	})
	t.Logf("SplitBlob (reads AC manifest) err=%v resp=%v", splitErr, splitResp)

	// Invariant: the CAS-write-only key must not have been able to create a
	// usable AC-keyspace entry. If SpliceBlob were gated like UpdateActionResult
	// (CACHE_WRITE required) there would be no manifest, and SplitBlob would
	// return NotFound. On the baseline SplitBlob succeeds -> this fails.
	require.Truef(t, status.IsNotFoundError(splitErr),
		"invariant violated: CAS-write-only key created a usable AC manifest entry via SpliceBlob "+
			"(SpliceBlob err=%v, SplitBlob err=%v, chunks=%v)", spliceErr, splitErr, splitResp.GetChunkDigests())
}
