//go:build linux && !android

package ext4_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	for _, prezeroed := range []bool{false, true} {
		t.Run(fmt.Sprintf("prezeroed=%t", prezeroed), func(t *testing.T) {
			flags.Set(t, "executor.reproducible_ext4_images", true)
			flags.Set(t, "executor.prezeroed_ext4_images", prezeroed)
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
		})
	}
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

func TestPrezeroedImages(t *testing.T) {
	flags.Set(t, "executor.prezeroed_ext4_images", true)
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprintf("populated=%t", populated), func(t *testing.T) {
			root := testfs.MakeTempDir(t)
			input := filepath.Join(root, "input")
			require.NoError(t, os.Mkdir(input, 0755))
			if populated {
				require.NoError(t, os.WriteFile(filepath.Join(input, "data"), []byte("preserved data"), 0751))
				require.NoError(t, os.Symlink("data", filepath.Join(input, "link")))
				require.NoError(t, os.Link(filepath.Join(input, "data"), filepath.Join(input, "hardlink")))
			}
			image := filepath.Join(root, "image.ext4")
			if populated {
				require.NoError(t, ext4.DirectoryToImage(context.Background(), input, image, 256<<20))
			} else {
				require.NoError(t, ext4.MakeEmptyImage(context.Background(), image, 256<<20))
			}
			info, err := os.Stat(image)
			require.NoError(t, err)
			require.Less(t, info.Sys().(*syscall.Stat_t).Blocks*512, int64(2<<20))
			out, err := exec.Command("/sbin/e2fsck", "-fn", image).CombinedOutput()
			require.NoError(t, err, "%s", out)
			header, err := exec.Command("/sbin/dumpe2fs", "-h", image).CombinedOutput()
			require.NoError(t, err)
			require.Contains(t, string(header), "has_journal")
			if populated {
				dest := testfs.MakeTempDir(t)
				require.NoError(t, ext4.ImageToDirectory(context.Background(), image, dest, []string{"/"}))
				data, err := os.ReadFile(filepath.Join(dest, "data"))
				require.NoError(t, err)
				require.Equal(t, "preserved data", string(data))
				target, err := os.Readlink(filepath.Join(dest, "link"))
				require.NoError(t, err)
				require.Equal(t, "data", target)
				// debugfs rdump doesn't preserve hardlink identity; inspect the image.
				a, err := exec.Command("/sbin/debugfs", "-R", "stat /data", image).CombinedOutput()
				require.NoError(t, err)
				b, err := exec.Command("/sbin/debugfs", "-R", "stat /hardlink", image).CombinedOutput()
				require.NoError(t, err)
				require.Equal(t, strings.Split(string(a), "\n")[1], strings.Split(string(b), "\n")[1])
			}
		})
	}
}

func TestPrezeroedImages_RejectExistingContents(t *testing.T) {
	flags.Set(t, "executor.prezeroed_ext4_images", true)
	root := testfs.MakeTempDir(t)
	image := filepath.Join(root, "existing")
	require.NoError(t, os.WriteFile(image, []byte("must not overwrite"), 0644))
	require.ErrorContains(t, ext4.MakeEmptyImage(context.Background(), image, 32<<20), "not empty")
	require.ErrorContains(t, ext4.DirectoryToImage(context.Background(), root, image, 32<<20), "not empty")
	data, err := os.ReadFile(image)
	require.NoError(t, err)
	require.Equal(t, "must not overwrite", string(data))
	require.Error(t, ext4.MakeEmptyImage(context.Background(), "/dev/null", 32<<20))
}
