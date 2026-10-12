//go:build linux && !android

package ext4_test

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/ext4"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testdigest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/util/disk"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

func randomDir(subDirs []string) string {
	pathParts := []string{}
	i := 0
	for {
		j := rand.Intn(len(subDirs) - i)
		if i+j >= len(subDirs)-1 {
			break
		}
		pathParts = append(pathParts, subDirs[i+j])
		i = i + j
	}
	return filepath.Join(pathParts...)
}

func TestE2E(t *testing.T) {
	ctx := context.Background()
	rootDir := testfs.MakeTempDir(t)
	allowedPaths := []string{"a", "b", "c", "d", "e", "f"}

	// Create a "random" looking directory full of random digests.
	// Save a map of path -> digest hash.
	pathHashMap := make(map[string]string, 0)
	for range 100 {
		parentDir := filepath.Join(rootDir, randomDir(allowedPaths))
		disk.EnsureDirectoryExists(parentDir)
		r, buf := testdigest.RandomCASResourceBuf(t, 1000)
		dest := filepath.Join(parentDir, r.GetDigest().GetHash())
		if err := os.WriteFile(dest, buf, 0644); err != nil {
			t.Fatal(err)
		}
		pathHashMap[dest] = r.GetDigest().GetHash()
	}

	// Make the random directory into an ext4 image.
	imageFile := testfs.MakeTempFile(t, "", "*.img")
	if err := ext4.DirectoryToImageAutoSize(ctx, rootDir, imageFile); err != nil {
		t.Fatal(err)
	}

	// Create a new (empty) directory and unpack the image file to the new directory.
	newRoot := testfs.MakeTempDir(t)
	if err := ext4.ImageToDirectory(ctx, imageFile, newRoot, []string{"/"}); err != nil {
		t.Fatal(err)
	}

	// Ensure that everything in the image, plus the few random files we
	// added manually, is in the new directory.
	for path, hash := range pathHashMap {
		newPath := strings.Replace(path, rootDir, newRoot, 1)
		f, err := os.Open(newPath)
		if err != nil {
			t.Fatal(err)
		}
		d, err := digest.Compute(f, repb.DigestFunction_SHA256)
		if err != nil {
			t.Fatal(err)
		}
		if d.GetHash() != hash {
			t.Fatalf("File content mismatch: %q != %q, %q", d.GetHash(), hash, newPath)
		}
	}
}

func TestDirectoryToImageAutoSize_NonExistentDir(t *testing.T) {
	ctx := context.Background()
	root := testfs.MakeTempDir(t)

	err := ext4.DirectoryToImageAutoSize(ctx, "/does/not/exist", filepath.Join(root, "/image.ext4"))

	require.Error(t, err)
}

func TestDirectoryToImageAutoSize_DanglingSymlink(t *testing.T) {
	ctx := context.Background()
	root := testfs.MakeTempDir(t)
	workspace := testfs.MakeDirAll(t, root, "workspace")
	err := os.Symlink("/does/not/exist", filepath.Join(workspace, "a"))
	require.NoError(t, err)

	err = ext4.DirectoryToImageAutoSize(ctx, workspace, filepath.Join(root, "workspace.ext4"))

	require.NoError(t, err)
}

func TestDirectoryToImageAutoSize_Reproducible(t *testing.T) {
	flags.Set(t, "executor.reproducible_ext4_images", true)
	ctx := t.Context()
	root := testfs.MakeTempDir(t)

	// Build a tree with a nested directory, a symlink, and files with
	// different mtimes, all of which end up in the image metadata. Like a
	// freshly extracted archive, the files keep their creation-time atimes.
	inputDir := testfs.MakeDirAll(t, root, "input")
	testfs.WriteAllFileContents(t, inputDir, map[string]string{
		"a.txt":     "hello",
		"dir/b.txt": "world",
	})
	err := os.Symlink("a.txt", filepath.Join(inputDir, "link"))
	require.NoError(t, err)
	err = os.Chtimes(filepath.Join(inputDir, "a.txt"), time.Now(), time.Unix(1_600_000_000, 0))
	require.NoError(t, err)

	firstImage := filepath.Join(root, "first.ext4")
	err = ext4.DirectoryToImageAutoSize(ctx, inputDir, firstImage)
	require.NoError(t, err)

	// Bump the atime of a file, which also updates its ctime, the way a fresh
	// extraction of the same content on another machine would. Converting the
	// tree again should still produce a byte-identical image, since these
	// timestamps carry no information about the content.
	b := filepath.Join(inputDir, "dir/b.txt")
	info, err := os.Stat(b)
	require.NoError(t, err)
	err = os.Chtimes(b, time.Now().Add(time.Hour), info.ModTime())
	require.NoError(t, err)

	secondImage := filepath.Join(root, "second.ext4")
	err = ext4.DirectoryToImageAutoSize(ctx, inputDir, secondImage)
	require.NoError(t, err)

	firstDigest := fileDigest(t, firstImage)
	secondDigest := fileDigest(t, secondImage)
	require.Equal(t, firstDigest, secondDigest)
}

func fileDigest(t *testing.T, path string) string {
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	d, err := digest.Compute(f, repb.DigestFunction_SHA256)
	require.NoError(t, err)
	return d.GetHash()
}

func TestImageToDirectory(t *testing.T) {
	for _, test := range []struct {
		name     string
		image    map[string]string
		paths    []string
		expected map[string]string
	}{
		{
			name: "passing the root path extracts the whole image",
			image: map[string]string{
				"foo/bar/hello.txt": "hello",
				"world.txt":         "world",
			},
			paths: []string{"/"},
			expected: map[string]string{
				"foo/bar/hello.txt": "hello",
				"world.txt":         "world",
				// ext4 recovery dir
				"lost+found": testfs.EmptyDir,
			},
		},
		{
			name: "file paths are allowed",
			image: map[string]string{
				"ignore.txt":         "IGNORE_ME",
				"foo/hello.txt":      "hello",
				"foo/ignore.txt":     "hello",
				"foo/bar/ignore.txt": "IGNORE_ME",
			},
			paths: []string{"/foo/hello.txt"},
			expected: map[string]string{
				"foo/hello.txt": "hello",
			},
		},
		{
			name: "directory paths are allowed",
			image: map[string]string{
				"parent1/child1/hello.txt":  "hello",
				"parent1/child2/ignore.txt": "IGNORE_ME",
				"parent2/world.txt":         "world",
			},
			paths: []string{"/parent1/child1", "/parent2/"},
			expected: map[string]string{
				"parent1/child1/hello.txt": "hello",
				"parent2/world.txt":        "world",
			},
		},
		{
			name: "nonexistent paths are silently ignored",
			image: map[string]string{
				"parent1/child1/hello.txt": "hello",
				"world.txt":                "world",
			},
			paths: []string{
				"/does_not_exist",
				"/parent1/does_not_exist",
				"/parent1/child1/hello.txt",
			},
			expected: map[string]string{
				"parent1/child1/hello.txt": "hello",
			},
		},
		{
			name: "empty directories are copied",
			image: map[string]string{
				"parent1/emptydir1": testfs.EmptyDir,
				"parent2/emptydir2": testfs.EmptyDir,
				"emptydir3":         testfs.EmptyDir,
			},
			paths: []string{"/parent1/emptydir1", "/parent2", "/emptydir3"},
			expected: map[string]string{
				"parent1/emptydir1": testfs.EmptyDir,
				"parent2/emptydir2": testfs.EmptyDir,
				"emptydir3":         testfs.EmptyDir,
			},
		},
		{
			name: "duplicate paths are allowed",
			image: map[string]string{
				"parent1/hello.txt": "hello",
				"parent2/world.txt": "world",
			},
			paths: []string{"/parent1", "/parent1/hello.txt", "/parent2", "/parent2"},
			expected: map[string]string{
				"parent1/hello.txt": "hello",
				"parent2/world.txt": "world",
			},
		},
		{
			name: "paths not beginning with slash are allowed",
			image: map[string]string{
				"parent1/hello.txt": "hello",
				"parent2/world.txt": "world",
			},
			paths: []string{"parent1"},
			expected: map[string]string{
				"parent1/hello.txt": "hello",
			},
		},
		{
			name: "paths beginning with dotslash are allowed",
			image: map[string]string{
				"parent1/hello.txt": "hello",
				"parent2/world.txt": "world",
			},
			paths: []string{"./parent1"},
			expected: map[string]string{
				"parent1/hello.txt": "hello",
			},
		},
		{
			name: "empty path is the same as root path",
			image: map[string]string{
				"foo/bar/hello.txt": "hello",
				"world.txt":         "world",
			},
			paths: []string{""},
			expected: map[string]string{
				"foo/bar/hello.txt": "hello",
				"world.txt":         "world",
				// ext4 recovery dir
				"lost+found": testfs.EmptyDir,
			},
		},
		{
			name: "dot is the same as root path",
			image: map[string]string{
				"foo/bar/hello.txt": "hello",
				"world.txt":         "world",
			},
			paths: []string{"."},
			expected: map[string]string{
				"foo/bar/hello.txt": "hello",
				"world.txt":         "world",
				// ext4 recovery dir
				"lost+found": testfs.EmptyDir,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			inputDir := testfs.MakeTempDir(t)
			testfs.WriteAllFileContents(t, inputDir, test.image)
			tmp := testfs.MakeTempDir(t)
			imagePath := filepath.Join(tmp, "image.ext4")
			err := ext4.DirectoryToImageAutoSize(ctx, inputDir, imagePath)
			require.NoError(t, err)
			outputDir := testfs.MakeTempDir(t)

			err = ext4.ImageToDirectory(ctx, imagePath, outputDir, test.paths)
			require.NoError(t, err)

			testfs.AssertExactFileContents(t, outputDir, test.expected)
		})
	}
}

func TestTarToImageAutoSize(t *testing.T) {
	out, err := exec.Command("/sbin/mke2fs", "-V").CombinedOutput()
	require.NoError(t, err)
	// The VM and old executor hosts may predate tar support. These cases are
	// tested separately below without depending on tar-capable mke2fs.
	var major, minor, patch int
	_, err = fmt.Sscanf(string(out), "mke2fs %d.%d.%d", &major, &minor, &patch)
	require.NoError(t, err)
	if major == 1 && (minor < 47 || minor == 47 && patch < 1) {
		t.Skip("mke2fs tar population requires 1.47.1+")
	}
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	headers := []*tar.Header{
		{Name: "parent/hard", Typeflag: tar.TypeLink, Linkname: "parent/file", Mode: 0640},
		{Name: "parent/file", Mode: 0640, Size: 3, Uid: 123, Gid: 456, ModTime: time.Unix(1600000000, 0)},
		{Name: "parent", Typeflag: tar.TypeDir, Mode: 0700, ModTime: time.Unix(1600000000, 0)},
		{Name: "sym", Typeflag: tar.TypeSymlink, Linkname: "parent/file", Mode: 0777},
		{Name: "dev/null", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3, Mode: 0666},
	}
	for _, h := range headers {
		require.NoError(t, tw.WriteHeader(h))
		if h.Size > 0 {
			_, err := tw.Write([]byte("abc"))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	for _, excludeDev := range []bool{false, true} {
		t.Run(fmt.Sprint(excludeDev), func(t *testing.T) {
			image := filepath.Join(t.TempDir(), "image.ext4")
			calls := 0
			err := ext4.TarToImageAutoSize(t.Context(), func() (io.ReadCloser, error) { calls++; return io.NopCloser(bytes.NewReader(b.Bytes())), nil }, image, excludeDev)
			require.NoError(t, err)
			require.Equal(t, 2, calls)
			out, err := exec.Command("/sbin/e2fsck", "-fn", image).CombinedOutput()
			require.NoError(t, err, "%s", out)
			stat := func(name string) string {
				out, err := exec.Command("/sbin/debugfs", "-R", "stat "+name, image).CombinedOutput()
				require.NoError(t, err)
				return string(out)
			}
			fileStat := stat("parent/file")
			require.Contains(t, fileStat, "Mode:  0640")
			require.Contains(t, fileStat, "User:   123")
			require.Contains(t, fileStat, "Group:   456")
			require.Contains(t, fileStat, "Links: 2")
			inode := func(s string) string {
				_, tail, ok := strings.Cut(s, "Inode:")
				require.True(t, ok, "%s", s)
				return strings.Fields(tail)[0]
			}
			require.Equal(t, inode(fileStat), inode(stat("parent/hard")))
			require.Contains(t, stat("parent"), "Mode:  0700")
			if excludeDev {
				require.Contains(t, stat("dev/null"), "File not found")
			} else {
				require.Contains(t, stat("dev/null"), "Type: character special")
			}
			dir := t.TempDir()
			require.NoError(t, ext4.ImageToDirectory(t.Context(), image, dir, []string{"/parent/file", "/sym"}))
			contents, err := os.ReadFile(filepath.Join(dir, "parent/file"))
			require.NoError(t, err)
			require.Equal(t, "abc", string(contents))
			target, err := os.Readlink(filepath.Join(dir, "sym"))
			require.NoError(t, err)
			require.Equal(t, "parent/file", target)
		})
	}
}

func TestTarToImageAutoSize_Errors(t *testing.T) {
	image := filepath.Join(t.TempDir(), "image.ext4")
	require.NoError(t, os.WriteFile(image, []byte("existing"), 0600))
	called := false
	err := ext4.TarToImageAutoSize(t.Context(), func() (io.ReadCloser, error) { called = true; return nil, fmt.Errorf("should not open") }, image, false)
	require.Error(t, err)
	require.False(t, called)
	require.NoError(t, os.Remove(image))
	flags.Set(t, "executor.reproducible_ext4_images", true)
	err = ext4.TarToImageAutoSize(t.Context(), func() (io.ReadCloser, error) { called = true; return nil, fmt.Errorf("should not open") }, image, false)
	require.ErrorContains(t, err, "incompatible")
	require.False(t, called)
	flags.Set(t, "executor.reproducible_ext4_images", false)
	err = ext4.TarToImageAutoSize(t.Context(), func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("bad tar")), nil }, image, false)
	require.ErrorContains(t, err, "estimate tar")
}

func TestTarToImageAutoSize_CancelBlockedRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	pr, pw := io.Pipe()
	defer pw.Close()
	image := filepath.Join(t.TempDir(), "image.ext4")
	opened := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- ext4.TarToImageAutoSize(ctx, func() (io.ReadCloser, error) { close(opened); return pr, nil }, image, false)
	}()
	<-opened
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("blocked source not closed on cancellation")
	}
}

func TestTarToImageAutoSize_RejectDirectoryMtimeChange(t *testing.T) {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, h := range []*tar.Header{{Name: "a", Typeflag: tar.TypeDir, Mode: 0755}, {Name: "b", Typeflag: tar.TypeDir, Mode: 0755}, {Name: "a/late", Mode: 0644}} {
		require.NoError(t, tw.WriteHeader(h))
	}
	require.NoError(t, tw.Close())
	image := filepath.Join(t.TempDir(), "image.ext4")
	err := ext4.TarToImageAutoSize(t.Context(), func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b.Bytes())), nil }, image, false)
	require.ErrorContains(t, err, "cannot preserve GNU tar directory mtimes")
	_, err = os.Stat(image)
	require.True(t, os.IsNotExist(err))
}
