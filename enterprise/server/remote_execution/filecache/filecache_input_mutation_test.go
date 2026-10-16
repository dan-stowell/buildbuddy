package filecache_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/filecache"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/util/claims"
	"github.com/buildbuddy-io/buildbuddy/server/util/fastcopy"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// These tests model the executor's input/output handling at the filecache
// level: a file is materialized in action workspace 1 and added to the
// filecache (as dirtools does after downloading an input, or after uploading
// an output), a process in workspace 1 then modifies that path in place, and
// a later action links the same digest into workspace 2 via FastLinkFile.
// The bytes seen in workspace 2 should always match the requested digest.

const mutationOriginal = "original-input-contents\n"

func mutationNode(t *testing.T, contents string, executable bool) *repb.FileNode {
	d, err := digest.Compute(strings.NewReader(contents), repb.DigestFunction_SHA256)
	require.NoError(t, err)
	return &repb.FileNode{Name: "in.txt", Digest: d, IsExecutable: executable}
}

func newMutationFileCache(t *testing.T) interfacesFileCache {
	fc, err := filecache.NewFileCache(testfs.MakeTempDir(t), 1_000_000_000, false)
	require.NoError(t, err)
	fc.WaitForDirectoryScanToComplete()
	return fc
}

// interfacesFileCache is the subset of the filecache API used here.
type interfacesFileCache interface {
	AddFile(ctx context.Context, node *repb.FileNode, existingFilePath string) error
	FastLinkFile(ctx context.Context, node *repb.FileNode, outputPath string) bool
}

func requireDigestMatches(t *testing.T, path string, node *repb.FileNode) {
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	d, err := digest.Compute(strings.NewReader(string(b)), repb.DigestFunction_SHA256)
	require.NoError(t, err)
	require.Equal(t, node.GetDigest().GetHash(), d.GetHash(),
		"file linked from filecache for digest %s/%d has different contents: %q",
		node.GetDigest().GetHash(), node.GetDigest().GetSizeBytes(), string(b))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, node.GetIsExecutable(), info.Mode()&0o100 != 0,
		"executable bit of linked file (mode %s) does not match FileNode.IsExecutable=%t", info.Mode(), node.GetIsExecutable())
}

type mutationCase struct {
	name       string
	executable bool
	mutate     func(t *testing.T, path string)
}

var mutationCases = []mutationCase{
	{
		name: "append",
		mutate: func(t *testing.T, path string) {
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			require.NoError(t, err)
			_, err = f.WriteString("appended-by-action-1\n")
			require.NoError(t, err)
			require.NoError(t, f.Close())
		},
	},
	{
		name: "truncate_and_overwrite",
		mutate: func(t *testing.T, path string) {
			// Equivalent of `cp other path` or `echo x > path`: O_TRUNC on
			// the existing inode.
			require.NoError(t, os.WriteFile(path, []byte("overwritten\n"), 0o644))
		},
	},
	{
		name:       "chmod_minus_x",
		executable: true,
		mutate: func(t *testing.T, path string) {
			require.NoError(t, os.Chmod(path, 0o644))
		},
	},
}

func linkAndMutate(t *testing.T, ctx context.Context, fc interfacesFileCache, tc mutationCase) (node *repb.FileNode, ws2Path string) {
	ws1 := testfs.MakeTempDir(t)
	ws2 := testfs.MakeTempDir(t)
	node = mutationNode(t, mutationOriginal, tc.executable)
	mode := os.FileMode(0o644)
	if tc.executable {
		mode = 0o755
	}
	in1 := filepath.Join(ws1, "in.txt")
	require.NoError(t, os.WriteFile(in1, []byte(mutationOriginal), mode))
	require.NoError(t, os.Chmod(in1, mode))
	// Action 1's input is published to the filecache after download.
	require.NoError(t, fc.AddFile(ctx, node, in1))
	// Action 1 modifies its input in place.
	tc.mutate(t, in1)
	// Action 2 requests the same digest.
	ws2Path = filepath.Join(ws2, "in.txt")
	return node, ws2Path
}

func TestFileCacheInputMutation_VisibleToLaterAction(t *testing.T) {
	for _, cfg := range []struct {
		name  string
		flags map[string]any
	}{
		{name: "default_flags"},
		{name: "always_clone", flags: map[string]any{"executor.local_cache_always_clone": true}},
		{name: "always_clone_reflinking", flags: map[string]any{"executor.local_cache_always_clone": true, "executor.enable_fastcopy_reflinking": true}},
	} {
		for _, group := range []string{"GR1234567890", "ANON"} {
			for _, tc := range mutationCases {
				t.Run(cfg.name+"/"+group+"/"+tc.name, func(t *testing.T) {
					for k, v := range cfg.flags {
						flags.Set(t, k, v)
					}
					if cfg.flags["executor.enable_fastcopy_reflinking"] == true {
						probeDir := testfs.MakeTempDir(t)
						src := filepath.Join(probeDir, "src")
						require.NoError(t, os.WriteFile(src, []byte("x"), 0o644))
						if err := fastcopy.Clone(src, filepath.Join(probeDir, "dst")); err != nil {
							t.Skipf("reflink not supported by this filesystem: %s", err)
						}
					}
					ctx := context.Background()
					if group != "ANON" {
						ctx = claims.AuthContextWithJWT(ctx, &claims.Claims{GroupID: group}, nil)
					}
					fc := newMutationFileCache(t)
					node, ws2Path := linkAndMutate(t, ctx, fc, tc)
					linked := fc.FastLinkFile(ctx, node, ws2Path)
					require.True(t, linked, "expected filecache hit")
					requireDigestMatches(t, ws2Path, node)
				})
			}
		}
	}
}

// Same as above, but the file enters the filecache as an action output (as
// UploadTree does) and is later rewritten in place, e.g. by a subsequent
// action on a recycled runner with a preserved workspace.
func TestFileCacheOutputMutation_VisibleToLaterAction(t *testing.T) {
	ctx := claims.AuthContextWithJWT(context.Background(), &claims.Claims{GroupID: "GR1234567890"}, nil)
	fc := newMutationFileCache(t)
	ws1 := testfs.MakeTempDir(t)
	out := filepath.Join(ws1, "out.txt")
	require.NoError(t, os.WriteFile(out, []byte(mutationOriginal), 0o644))
	node := mutationNode(t, mutationOriginal, false)
	require.NoError(t, fc.AddFile(ctx, node, out))

	f, err := os.OpenFile(out, os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = f.WriteString("next action appended\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	ws2Path := filepath.Join(testfs.MakeTempDir(t), "in.txt")
	require.True(t, fc.FastLinkFile(ctx, node, ws2Path))
	requireDigestMatches(t, ws2Path, node)
}

// Control: replacing the input via write-to-temp + rename (what `sed -i`,
// most compilers and editors do) creates a new inode and leaves the cached
// file intact. Expected to pass.
func TestFileCacheInputReplaceByRename_NotVisibleToLaterAction(t *testing.T) {
	ctx := claims.AuthContextWithJWT(context.Background(), &claims.Claims{GroupID: "GR1234567890"}, nil)
	fc := newMutationFileCache(t)
	node, ws2Path := linkAndMutate(t, ctx, fc, mutationCase{
		name: "rename",
		mutate: func(t *testing.T, path string) {
			tmp := path + ".tmp"
			require.NoError(t, os.WriteFile(tmp, []byte("replaced\n"), 0o644))
			require.NoError(t, os.Rename(tmp, path))
		},
	})
	require.True(t, fc.FastLinkFile(ctx, node, ws2Path))
	requireDigestMatches(t, ws2Path, node)
}

// Control: the filecache is partitioned by group, so a mutation in one group
// is not visible to another group (or to anonymous users). Expected to pass.
func TestFileCacheInputMutation_NotVisibleAcrossGroups(t *testing.T) {
	ctx1 := claims.AuthContextWithJWT(context.Background(), &claims.Claims{GroupID: "GR1111111111"}, nil)
	ctx2 := claims.AuthContextWithJWT(context.Background(), &claims.Claims{GroupID: "GR2222222222"}, nil)
	fc := newMutationFileCache(t)
	node, ws2Path := linkAndMutate(t, ctx1, fc, mutationCases[0])
	require.False(t, fc.FastLinkFile(ctx2, node, ws2Path), "other group should miss")
	require.False(t, fc.FastLinkFile(context.Background(), node, ws2Path), "anonymous should miss")
	require.True(t, fc.FastLinkFile(ctx1, node, ws2Path), "same group should hit")
}
