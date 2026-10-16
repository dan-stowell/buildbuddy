package dirtools_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/cache/dirtools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

func singleFileTree(name string, d *repb.Digest) *repb.Tree {
	return &repb.Tree{Root: &repb.Directory{Files: []*repb.FileNode{{Name: name, Digest: d}}}}
}

func requireContentsMatchDigest(t *testing.T, path string, want *repb.Digest) {
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	got, err := digest.Compute(strings.NewReader(string(b)), repb.DigestFunction_SHA256)
	require.NoError(t, err)
	require.Equal(t, want.GetHash(), got.GetHash(),
		"input %q materialized for digest %s/%d has contents %q", path, want.GetHash(), want.GetSizeBytes(), string(b))
}

// Action 1 downloads input in.txt (which is published to the filecache), then
// appends to it. Action 2 has a different command but an input with the same
// digest; DownloadTree links it from the filecache.
func TestDownloadTree_InputModifiedByPreviousAction(t *testing.T) {
	env, ctx := testEnv(t)
	const contents = "hello from CAS\n"
	d := setFile(t, env, ctx, "", contents)
	tree := singleFileTree("in.txt", d)

	ws1 := testfs.MakeTempDir(t)
	info, err := dirtools.DownloadTree(ctx, env, "", repb.DigestFunction_SHA256, tree, &dirtools.DownloadTreeOpts{RootDir: ws1})
	require.NoError(t, err)
	require.Equal(t, int64(1), info.FileCount, "action 1 should download in.txt from CAS")
	requireContentsMatchDigest(t, filepath.Join(ws1, "in.txt"), d)

	// Action 1: `echo modified >> in.txt`
	f, err := os.OpenFile(filepath.Join(ws1, "in.txt"), os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = f.WriteString("modified by action 1\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// Action 2: same input digest, fresh workspace.
	ws2 := testfs.MakeTempDir(t)
	info, err = dirtools.DownloadTree(ctx, env, "", repb.DigestFunction_SHA256, tree, &dirtools.DownloadTreeOpts{RootDir: ws2})
	require.NoError(t, err)
	t.Logf("action 2: files downloaded=%d, bytes=%d", info.FileCount, info.BytesTransferred)
	requireContentsMatchDigest(t, filepath.Join(ws2, "in.txt"), d)
}

// An output uploaded with addToFileCache=true is hardlinked into the filecache.
// If the file is then rewritten in place (e.g. by the next action in a
// preserved workspace), later actions consuming that digest see the new bytes.
func TestUploadTree_OutputModifiedAfterUpload(t *testing.T) {
	env, ctx := testEnv(t)
	const contents = "output contents\n"
	ws1 := testfs.MakeTempDir(t)
	testfs.WriteAllFileContents(t, ws1, map[string]string{"out.txt": contents})
	cmd := &repb.Command{OutputPaths: []string{"out.txt"}}
	dirHelper := dirtools.NewDirHelper(ws1, cmd, fs.FileMode(0o755))
	ar := &repb.ActionResult{}
	_, err := dirtools.UploadTree(ctx, env, dirHelper, "", repb.DigestFunction_SHA256, ws1, cmd, ar, true /*=addToFileCache*/, nil)
	require.NoError(t, err)
	require.Len(t, ar.GetOutputFiles(), 1)
	d := ar.GetOutputFiles()[0].GetDigest()

	f, err := os.OpenFile(filepath.Join(ws1, "out.txt"), os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = f.WriteString("appended later\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	ws2 := testfs.MakeTempDir(t)
	info, err := dirtools.DownloadTree(ctx, env, "", repb.DigestFunction_SHA256, singleFileTree("in.txt", d), &dirtools.DownloadTreeOpts{RootDir: ws2})
	require.NoError(t, err)
	t.Logf("action 2: files downloaded=%d, bytes=%d", info.FileCount, info.BytesTransferred)
	requireContentsMatchDigest(t, filepath.Join(ws2, "in.txt"), d)
}
