package ociconv

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

func TestStreamingFallbackMetadata(t *testing.T) {
	// rootEligible exercises the archive guard even on unprivileged test workers.
	// Archive symlink ownership equals the worker's, avoiding root-only extraction.
	for _, rootEligible := range []bool{true, false} {
		t.Run(fmt.Sprintf("root_eligible_%t", rootEligible), func(t *testing.T) {
			if !rootEligible && os.Geteuid() == 0 {
				t.Skip("non-root comparison requires an unprivileged test worker")
			}
			var b bytes.Buffer
			tw := tar.NewWriter(&b)
			uid, gid := os.Geteuid(), os.Getegid()
			headers := []*tar.Header{
				{Name: "file", Typeflag: tar.TypeReg, Mode: 0644, Uid: uid, Gid: gid},
				{Name: "sym", Typeflag: tar.TypeSymlink, Linkname: "file", Mode: 0100, Uid: uid, Gid: gid},
				{Name: "hard", Typeflag: tar.TypeLink, Linkname: "sym", Mode: 0777, Uid: uid, Gid: gid},
			}
			if !rootEligible {
				// No unsupported hardlinks: only the non-root guard can trigger fallback.
				headers = []*tar.Header{{Name: "file", Typeflag: tar.TypeReg, Mode: 0777, Uid: 123, Gid: 456}}
			}
			for _, h := range headers {
				if err := tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b.Bytes())), nil })
			if err != nil {
				t.Fatal(err)
			}
			img, err := mutate.AppendLayers(empty.Image, layer)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			// Observe the actual baseline subprocess rather than inferring fallback from
			// output alone. Wrapping tar leaves its extraction behavior unchanged.
			realTar, err := exec.LookPath("tar")
			if err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(root, "tar-calls")
			wrapper := "#!/bin/sh\necho called >> '" + marker + "'\nexec '" + realTar + "' \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "tar"), []byte(wrapper), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			old := *streamExt4Images
			t.Cleanup(func() { *streamExt4Images = old })
			baseline, candidate := filepath.Join(root, "baseline.ext4"), filepath.Join(root, "candidate.ext4")
			*streamExt4Images = false
			if err := convertContainerToExt4FS(t.Context(), img, root, "baseline", baseline, rootEligible); err != nil {
				t.Fatal(err)
			}
			*streamExt4Images = true
			if rootEligible {
				// Exercise archive fallback as a root-eligible executor.
				err = convertContainerToExt4FS(t.Context(), img, root, "candidate", candidate, true)
			} else {
				// Exercise the public entrypoint's actual effective-UID check.
				err = ConvertContainerToExt4FS(t.Context(), img, root, "candidate", candidate)
			}
			if err != nil {
				t.Fatal(err)
			}
			calls, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(calls), "called") != 2 {
				t.Fatalf("expected both conversions to unpack: %s", calls)
			}
			stat := func(image, name string) string {
				out, err := exec.Command("/sbin/debugfs", "-R", "stat "+name, image).CombinedOutput()
				if err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				return string(out)
			}
			if rootEligible {
				re := regexp.MustCompile(`Inode:\s+(\d+)`)
				a, z := re.FindStringSubmatch(stat(candidate, "sym")), re.FindStringSubmatch(stat(candidate, "hard"))
				if len(a) != 2 || len(z) != 2 || a[1] == z[1] {
					t.Fatal("fallback must produce distinct symlink inodes")
				}
			}
			for _, h := range headers {
				a, z := stat(baseline, h.Name), stat(candidate, h.Name)
				for _, pattern := range []string{`Type:\s+\w+`, `Mode:\s+[0-7]+`, `User:\s+\d+\s+Group:\s+\d+`, `Links:\s+\d+`, `Fast link dest:.*`} {
					re := regexp.MustCompile(pattern)
					if re.FindString(a) != re.FindString(z) {
						t.Fatalf("%s mismatch for %s:\n%s\n%s", pattern, h.Name, a, z)
					}
				}
				if rootEligible && h.Typeflag != tar.TypeReg && !strings.Contains(z, "Links: 1") {
					t.Fatalf("symlink is not independent: %s", z)
				}
				if !rootEligible {
					re := regexp.MustCompile(`User:\s+(\d+)\s+Group:\s+(\d+)`)
					match := re.FindStringSubmatch(z)
					if len(match) != 3 || match[1] != strconv.Itoa(uid) || match[2] != strconv.Itoa(gid) {
						t.Fatalf("extracting user ownership not preserved: %s", z)
					}
				}
			}
		})
	}
}
