package action_cache_server_test

import (
	"context"
	"strings"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/action_cache_server"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/cachetools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	bspb "google.golang.org/genproto/googleapis/bytestream"
)

// getACWithAbsentBlob stores an ActionResult whose output file is present in the
// CAS, plus one extra digest reference (output file / stdout / stderr) that
// points at a blob that was never written to the CAS. It returns the result of
// GetActionResult for that entry.
func getACWithAbsentBlob(t *testing.T, field string) (*repb.ActionResult, error) {
	ctx := context.Background()
	te := testenv.GetTestEnv(t)
	conn := runACServer(ctx, t, te)
	acClient := repb.NewActionCacheClient(conn)
	bsClient := bspb.NewByteStreamClient(conn)

	presentDigest, err := cachetools.UploadBlobToCAS(ctx, bsClient, "", repb.DigestFunction_SHA256, []byte("present output"))
	require.NoError(t, err)
	absentDigest, err := digest.Compute(strings.NewReader("this blob was never uploaded"), repb.DigestFunction_SHA256)
	require.NoError(t, err)

	ar := &repb.ActionResult{
		OutputFiles: []*repb.OutputFile{{Path: "out/present", Digest: presentDigest}},
	}
	switch field {
	case "output_file":
		ar.OutputFiles = append(ar.OutputFiles, &repb.OutputFile{Path: "out/absent", Digest: absentDigest})
	case "stdout":
		ar.StdoutDigest = absentDigest
	case "stderr":
		ar.StderrDigest = absentDigest
	default:
		t.Fatalf("unknown field %q", field)
	}

	actionDigest := &repb.Digest{Hash: strings.Repeat("c", 64), SizeBytes: 42}
	_, err = acClient.UpdateActionResult(ctx, &repb.UpdateActionResultRequest{
		ActionDigest:   actionDigest,
		DigestFunction: repb.DigestFunction_SHA256,
		ActionResult:   ar,
	})
	require.NoError(t, err)

	return acClient.GetActionResult(ctx, &repb.GetActionResultRequest{
		ActionDigest:   actionDigest,
		DigestFunction: repb.DigestFunction_SHA256,
	})
}

// Control: an AC entry whose output file blob is absent is reported as a
// cache miss.
func TestGetActionResult_AbsentOutputFileIsMiss(t *testing.T) {
	_, err := getACWithAbsentBlob(t, "output_file")
	require.True(t, status.IsNotFoundError(err), "expected NotFound, got %v", err)
}

// The same AC entry, but with the absent blob referenced via stdout_digest,
// should also be reported as a miss (REAPI: servers SHOULD ensure that any
// blobs referenced from the CAS are available when returning an
// ActionResult).
func TestGetActionResult_AbsentStdoutIsMiss(t *testing.T) {
	rsp, err := getACWithAbsentBlob(t, "stdout")
	require.True(t, status.IsNotFoundError(err), "expected NotFound, got hit with stdout_digest=%v err=%v", rsp.GetStdoutDigest(), err)
}

func TestGetActionResult_AbsentStderrIsMiss(t *testing.T) {
	rsp, err := getACWithAbsentBlob(t, "stderr")
	require.True(t, status.IsNotFoundError(err), "expected NotFound, got hit with stderr_digest=%v err=%v", rsp.GetStderrDigest(), err)
}

// Direct check on the exported validation helper.
func TestValidateActionResult_AbsentStdoutStderr(t *testing.T) {
	ctx := context.Background()
	te := testenv.GetTestEnv(t)
	absentDigest, err := digest.Compute(strings.NewReader("never uploaded either"), repb.DigestFunction_SHA256)
	require.NoError(t, err)
	for _, ar := range []*repb.ActionResult{
		{OutputFiles: []*repb.OutputFile{{Path: "f", Digest: absentDigest}}},
		{StdoutDigest: absentDigest},
		{StderrDigest: absentDigest},
	} {
		err := action_cache_server.ValidateActionResult(ctx, te.GetCache(), "", repb.DigestFunction_SHA256, ar)
		require.Error(t, err, "ValidateActionResult accepted %v although a referenced blob is absent", ar)
	}
}
