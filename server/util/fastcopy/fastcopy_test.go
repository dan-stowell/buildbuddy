package fastcopy_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testshell"
	"github.com/buildbuddy-io/buildbuddy/server/util/fastcopy"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
)

func TestFastCopyDirFail(t *testing.T) {
	ws := testfs.MakeTempDir(t)
	err := os.Mkdir(path.Join(ws, "foo"), 0o777)
	require.NoError(t, err)
	target := path.Join(ws, "bar")

	_, err = os.Stat(target)
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	err = fastcopy.FastCopy(ws, target)
	require.Error(t, err, "should not be able to copy a directory")
}

func TestFastCopyFile(t *testing.T) {
	ws := testfs.MakeTempDir(t)
	source := testfs.MakeTempFile(t, ws, "foo")
	target := path.Join(ws, "bar")

	_, err := os.Stat(target)
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	err = fastcopy.FastCopy(source, target)
	require.NoError(t, err)

	_, err = os.Stat(target)
	require.NoError(t, err, "target file should exist")
}

func TestFastCopyFileExist(t *testing.T) {
	ws := testfs.MakeTempDir(t)
	source := testfs.MakeTempFile(t, ws, "foo")
	target := testfs.MakeTempFile(t, ws, "bar")

	before, err := os.Stat(target)
	require.NoError(t, err)

	err = fastcopy.FastCopy(source, target)
	require.NoError(t, err)

	after, err := os.Stat(target)
	require.NoError(t, err)

	require.Equal(t, before.ModTime(), after.ModTime(), "target should not have been modified")
}

func TestFastCopyDarwinForceHardlinks(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("test runs on macOS only")
	}

	ws := testfs.MakeTempDir(t)
	source := testfs.MakeTempFile(t, ws, "source")

	clone := path.Join(ws, "clone")
	require.NoError(t, fastcopy.FastCopy(source, clone))
	sourceInfo, err := os.Stat(source)
	require.NoError(t, err)
	cloneInfo, err := os.Stat(clone)
	require.NoError(t, err)
	require.False(t, os.SameFile(sourceInfo, cloneInfo))

	flags.Set(t, "executor.local_cache_use_hardlinks", true)
	hardlink := path.Join(ws, "hardlink")
	require.NoError(t, fastcopy.FastCopy(source, hardlink))
	hardlinkInfo, err := os.Stat(hardlink)
	require.NoError(t, err)
	require.True(t, os.SameFile(sourceInfo, hardlinkInfo))

	forcedClone := path.Join(ws, "forced-clone")
	require.NoError(t, fastcopy.Clone(source, forcedClone))
	forcedCloneInfo, err := os.Stat(forcedClone)
	require.NoError(t, err)
	require.False(t, os.SameFile(sourceInfo, forcedCloneInfo))
}

func TestFastCopySymlink(t *testing.T) {
	ws := testfs.MakeTempDir(t)
	linkTarget := testfs.MakeTempFile(t, ws, "foo")
	source := path.Join(ws, "fooLink")
	err := os.Symlink(linkTarget, source)
	require.NoError(t, err)

	target := path.Join(ws, "bar")

	_, err = os.Stat(target)
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	err = fastcopy.FastCopy(source, target)
	require.NoError(t, err)

	_, err = os.Stat(target)
	require.NoError(t, err, "target symlink should exist")
}

func TestFastCopyDarwinHardlinksSymlinkWithoutFollowing(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("test runs on macOS only")
	}
	flags.Set(t, "executor.local_cache_use_hardlinks", true)

	for _, dangling := range []bool{false, true} {
		t.Run(fmt.Sprintf("dangling=%t", dangling), func(t *testing.T) {
			ws := testfs.MakeTempDir(t)
			linkTarget := path.Join(ws, "link-target")
			if !dangling {
				testfs.MakeTempFile(t, ws, "link-target")
			}
			source := path.Join(ws, "source")
			require.NoError(t, os.Symlink(linkTarget, source))

			target := path.Join(ws, "target")
			require.NoError(t, fastcopy.FastCopy(source, target))

			sourceInfo, err := os.Lstat(source)
			require.NoError(t, err)
			targetInfo, err := os.Lstat(target)
			require.NoError(t, err)
			require.True(t, targetInfo.Mode()&os.ModeSymlink != 0, "target should be a symlink")
			require.True(t, os.SameFile(sourceInfo, targetInfo), "source and target should hardlink the same symlink inode")
			actualLinkTarget, err := os.Readlink(target)
			require.NoError(t, err)
			require.Equal(t, linkTarget, actualLinkTarget)
		})
	}
}

func TestFastCopyXFSReflink(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skipf("test requires mount() privileges")
	}
	if runtime.GOOS != "linux" {
		t.Skipf("test runs on linux only")
	}

	if _, err := exec.LookPath("mkfs.xfs"); err != nil {
		t.Skipf("test requires xfsprogs")
	}

	flags.Set(t, "executor.enable_fastcopy_reflinking", true)

	root := testfs.MakeTempDir(t)
	mnt := filepath.Join(root, "mnt")

	testshell.Run(t, root, `
		dd if=/dev/zero count=1000000 > ./disk.xfs
		mkfs.xfs ./disk.xfs
		mkdir `+mnt+`
		mount -o loop ./disk.xfs `+mnt+`
	`)
	t.Cleanup(func() {
		testshell.Run(t, root, `umount --force `+mnt)
	})

	src := filepath.Join(mnt, "src.txt")
	dst := src + ".reflink"
	err := os.WriteFile(src, nil, 0644)
	require.NoError(t, err)

	err = fastcopy.FastCopy(src, dst)
	require.NoError(t, err)

	// Try overwriting dst; should not affect the original.
	f, err := os.OpenFile(dst, os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	_, err = f.Write([]byte{1})
	require.NoError(t, err)

	b, err := os.ReadFile(src)
	require.NoError(t, err)
	require.Equal(t, []byte{}, b)
}

// Exercise both the copy fallback and the optional reflink path. Neither is
// allowed to share a writable inode with the source.
func TestCloneOrCopyIsolation(t *testing.T) {
	for _, reflink := range []bool{false, true} {
		t.Run(fmt.Sprintf("reflink=%t", reflink), func(t *testing.T) {
			flags.Set(t, "executor.enable_fastcopy_reflinking", reflink)
			dir := testfs.MakeTempDir(t)
			src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
			require.NoError(t, os.WriteFile(src, []byte("original"), 0755))
			require.NoError(t, fastcopy.CloneOrCopy(src, dst))
			si, err := os.Stat(src)
			require.NoError(t, err)
			di, err := os.Stat(dst)
			require.NoError(t, err)
			require.False(t, os.SameFile(si, di))
			require.Equal(t, si.Mode(), di.Mode())
			require.NoError(t, os.WriteFile(dst, []byte("modified"), 0644))
			require.NoError(t, os.Chmod(dst, 0600))
			contents, err := os.ReadFile(src)
			require.NoError(t, err)
			require.Equal(t, "original", string(contents))
			siAfter, err := os.Stat(src)
			require.NoError(t, err)
			require.Equal(t, si.Mode(), siAfter.Mode())
		})
	}
}

func TestCloneOrCopyDoesNotOverwrite(t *testing.T) {
	flags.Set(t, "executor.enable_fastcopy_reflinking", false)
	dir := testfs.MakeTempDir(t)
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	require.NoError(t, os.WriteFile(src, []byte("source"), 0644))
	require.NoError(t, os.WriteFile(dst, []byte("destination"), 0644))
	require.ErrorIs(t, fastcopy.CloneOrCopy(src, dst), os.ErrExist)
	contents, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "destination", string(contents))
}
