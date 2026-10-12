package ociconv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/filecache"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/ext4"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Root-only integration check supplements real images, which may have no
// device nodes or xattrs. Publish a frozen ext4 fixture through all modes and
// verify exact bytes, including the filesystem's generated timestamps.
func TestPublicationMetadata(t *testing.T) {
	if os.Getenv("OCICONV_METADATA_CHECK") != "1" {
		t.Skip("opt-in mounted metadata fixture")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root to create device nodes and mount images")
	}
	ctx := context.Background()
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	require.NoError(t, os.Mkdir(tree, 0755))
	payload := filepath.Join(tree, "payload")
	require.NoError(t, os.WriteFile(payload, []byte("contents\n"), 0751))
	require.NoError(t, os.Chmod(payload, 0751))
	require.NoError(t, os.Chown(payload, 1234, 2345))
	require.NoError(t, os.Link(payload, filepath.Join(tree, "hardlink")))
	require.NoError(t, os.Symlink("payload", filepath.Join(tree, "symlink")))
	require.NoError(t, unix.Setxattr(payload, "user.ociconv", []byte("fixture"), 0))
	stamp := time.Unix(1234567890, 0)
	require.NoError(t, os.Chtimes(payload, stamp, stamp))
	require.NoError(t, unix.Mknod(filepath.Join(tree, "device"), unix.S_IFCHR|0600, int(unix.Mkdev(1, 3))))
	baseline := filepath.Join(root, "baseline.ext4")
	require.NoError(t, ext4.DirectoryToImageAutoSize(ctx, tree, baseline))
	baselineHash, err := hashFile(baseline)
	require.NoError(t, err)
	check := func(t *testing.T, image string) {
		t.Helper()
		out, err := exec.Command("e2fsck", "-fn", image).CombinedOutput()
		require.NoError(t, err, "%s", out)
		mount := filepath.Join(root, "mount")
		require.NoError(t, os.MkdirAll(mount, 0755))
		out, err = exec.Command("mount", "-o", "loop,ro", image, mount).CombinedOutput()
		require.NoError(t, err, "%s", out)
		defer func() { require.NoError(t, exec.Command("umount", mount).Run()) }()
		a, err := os.Stat(filepath.Join(mount, "payload"))
		require.NoError(t, err)
		b, err := os.Stat(filepath.Join(mount, "hardlink"))
		require.NoError(t, err)
		require.True(t, os.SameFile(a, b))
		require.Equal(t, os.FileMode(0751), a.Mode().Perm())
		require.Equal(t, stamp.Unix(), a.ModTime().Unix())
		require.Equal(t, uint32(1234), a.Sys().(*syscall.Stat_t).Uid)
		require.Equal(t, uint32(2345), a.Sys().(*syscall.Stat_t).Gid)
		content, err := os.ReadFile(filepath.Join(mount, "payload"))
		require.NoError(t, err)
		require.Equal(t, "contents\n", string(content))
		target, err := os.Readlink(filepath.Join(mount, "symlink"))
		require.NoError(t, err)
		require.Equal(t, "payload", target)
		buf := make([]byte, 64)
		n, err := unix.Getxattr(filepath.Join(mount, "payload"), "user.ociconv", buf)
		require.NoError(t, err)
		require.Equal(t, "fixture", string(buf[:n]))
		device, err := os.Lstat(filepath.Join(mount, "device"))
		require.NoError(t, err)
		require.NotZero(t, device.Mode()&os.ModeCharDevice)
		require.Equal(t, unix.Mkdev(1, 3), device.Sys().(*syscall.Stat_t).Rdev)
	}
	check(t, baseline)
	for _, config := range []string{"filecache-baseline", "filecache-local-temp", "legacy-hash", "legacy-manifest"} {
		t.Run(config, func(t *testing.T) {
			cacheRoot := t.TempDir()
			fc, err := filecache.NewFileCache(filepath.Join(cacheRoot, "cache"), 1_000_000_000, false)
			require.NoError(t, err)
			defer fc.Close()
			flags.Set(t, "executor.local_cache_store_ext4_images", config == "filecache-baseline" || config == "filecache-local-temp")
			flags.Set(t, "executor.ext4_image_temp_in_filecache", config == "filecache-local-temp")
			flags.Set(t, "executor.legacy_ext4_use_manifest_digest", config == "legacy-manifest")
			tmp, err := createTempDiskImage(fc, cacheRoot)
			require.NoError(t, err)
			require.NoError(t, tmp.Close())
			require.NoError(t, os.Remove(tmp.Name()))
			require.NoError(t, os.Link(baseline, tmp.Name()))
			defer os.Remove(tmp.Name())
			_, err = publishDiskImage(ctx, empty.Image, fc, cacheRoot, "metadata-fixture", tmp.Name())
			require.NoError(t, err)
			output := filepath.Join(cacheRoot, "output.ext4")
			require.NoError(t, LinkCachedImage(ctx, fc, cacheRoot, "metadata-fixture", output))
			got, err := hashFile(output)
			require.NoError(t, err)
			require.Equal(t, baselineHash, got, "publication preserves every ext4 byte, including all inode metadata")
			check(t, output)
		})
	}
}
