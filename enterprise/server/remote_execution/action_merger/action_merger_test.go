package action_merger

import (
	"context"
	"strings"
	"testing"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/stretchr/testify/require"
)

func TestDigestFunctionPendingExecutionKeys(t *testing.T) {
	env := testenv.GetTestEnv(t)
	ctx, err := prefix.AttachUserPrefixToContext(context.Background(), env.GetAuthenticator())
	require.NoError(t, err)
	d := &repb.Digest{Hash: strings.Repeat("a", 64), SizeBytes: 1}
	sha, err := redisKeyForPendingExecutionID(ctx, digest.NewCASResourceName(d, "instance", repb.DigestFunction_SHA256))
	require.NoError(t, err)
	blake, err := redisKeyForPendingExecutionID(ctx, digest.NewCASResourceName(d, "instance", repb.DigestFunction_BLAKE3))
	require.NoError(t, err)
	require.NotEqual(t, sha, blake)
	again, err := redisKeyForPendingExecutionID(ctx, digest.NewCASResourceName(d, "instance", repb.DigestFunction_BLAKE3))
	require.NoError(t, err)
	require.Equal(t, blake, again)
}
