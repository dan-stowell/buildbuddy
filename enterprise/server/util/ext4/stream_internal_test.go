package ext4

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStreamingMetadata(t *testing.T) {
	if os.Getenv("EXT4_METADATA_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestStreamingMetadata$")
		cmd.Env = append(os.Environ(), "EXT4_METADATA_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return
	}
	syscall.Umask(0077) // Isolated subprocess: never changes other tests' umask.
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, h := range []*tar.Header{
		{Name: "implicit/nested/file", Mode: 0644, Typeflag: tar.TypeReg},
		{Name: "link", Mode: 0100, Typeflag: tar.TypeSymlink, Linkname: "implicit/nested/file"},
		{Name: "explicit", Mode: 0755, Typeflag: tar.TypeDir},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	unpack := filepath.Join(dir, "unpack")
	if err := os.Mkdir(unpack, 0700); err != nil {
		t.Fatal(err)
	}
	// Executors extract as root; --same-permissions reproduces root defaults
	// when the test runner is unprivileged. Implicit parents still use umask.
	cmd := exec.Command("tar", "--extract", "--same-permissions", "--directory", unpack)
	cmd.Stdin = bytes.NewReader(b.Bytes())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	baseline, streaming := filepath.Join(dir, "baseline.ext4"), filepath.Join(dir, "stream.ext4")
	if err := DirectoryToImageAutoSize(t.Context(), unpack, baseline); err != nil {
		t.Fatal(err)
	}
	if err := TarToImageAutoSize(t.Context(), func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b.Bytes())), nil }, streaming, false); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]string{"implicit": "0700", "implicit/nested": "0700", "explicit": "0755", "link": "0777"} {
		for _, image := range []string{baseline, streaming} {
			out, err := exec.Command("/sbin/debugfs", "-R", "stat "+name, image).CombinedOutput()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), "Mode:  "+mode) {
				t.Fatalf("%s %s expected %s: %s", image, name, mode, out)
			}
		}
	}
}

type observedReader struct {
	io.ReadCloser
	read chan struct{}
}

func (r *observedReader) Read(p []byte) (int, error) {
	select {
	case r.read <- struct{}{}:
	default:
	}
	return r.ReadCloser.Read(p)
}

func TestStreamingProcessCleanup(t *testing.T) {
	for _, cancelPopulation := range []bool{false, true} {
		t.Run(map[bool]string{false: "early_failure", true: "cancel_population"}[cancelPopulation], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var archive bytes.Buffer
			tw := tar.NewWriter(&archive)
			if err := tw.WriteHeader(&tar.Header{Name: "file", Mode: 0644, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			pr, pw := io.Pipe()
			populationRead := make(chan struct{}, 1)
			defer pw.Close()
			output := filepath.Join(t.TempDir(), "partial.ext4")
			started := make(chan *exec.Cmd, 1)
			calls := 0
			done := make(chan error, 1)
			go func() {
				done <- tarToImageAutoSize(ctx, func() (io.ReadCloser, error) {
					calls++
					if calls == 1 {
						return io.NopCloser(bytes.NewReader(archive.Bytes())), nil
					}
					return &observedReader{ReadCloser: pr, read: populationRead}, nil
				}, output, false, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					script := "echo partial > \"$1\"; exit 42"
					if cancelPopulation {
						script = "echo partial > \"$1\"; exec cat"
					}
					cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script, "test", output)
					started <- cmd
					return cmd
				})
			}()
			cmd := <-started
			if cancelPopulation {
				select {
				case <-populationRead:
				case <-time.After(5 * time.Second):
					t.Fatal("population did not read source")
				}
				deadline := time.After(5 * time.Second)
				for {
					if _, err := os.Stat(output); err == nil {
						break
					}
					select {
					case <-deadline:
						t.Fatal("child did not create output")
					case <-time.After(time.Millisecond * 10):
					}
				}
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected failure")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("population did not terminate")
			}
			if cmd.ProcessState == nil || !cmd.ProcessState.Exited() && !cancelPopulation {
				t.Fatal("child not reaped")
			}
			if err := cmd.Process.Signal(syscall.Signal(0)); err == nil {
				t.Fatal("child still running")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("partial image remains: %v", err)
			}
			if _, err := pw.Write([]byte("x")); err == nil {
				t.Fatal("source reader was not closed")
			}
		})
	}
}

// Compare the actual directory-populated and tar-populated filesystems, rather
// than only inspecting normalized headers. Expanded zero ranges model the
// flattened OCI payload of a sparse layer file.
func TestStreamingBaselineArchiveFeatures(t *testing.T) {
	payload := make([]byte, 8*1024*1024)
	copy(payload, "start")
	copy(payload[len(payload)-3:], "end")
	cases := []struct {
		name   string
		header tar.Header
		data   []byte
	}{
		{"sparse", tar.Header{Name: "sparse", Format: tar.FormatPAX}, payload},
		{"gnu_long_name", tar.Header{Name: strings.Repeat("g", 150), Format: tar.FormatGNU}, []byte("GNU long name")},
		{"pax_long_name", tar.Header{Name: strings.Repeat("p", 200), Format: tar.FormatPAX}, []byte("PAX long name")},
		{"xattrs", tar.Header{Name: "attrs", Format: tar.FormatPAX, Xattrs: map[string]string{
			"user.test": "archive-only", "security.capability": string([]byte{0, 0, 0, 2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}),
		}}, []byte("xattrs are omitted by baseline")},
		{"fractional_mtime", tar.Header{Name: "fractional", Format: tar.FormatPAX}, []byte("fractional")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.header
			h.Mode, h.Typeflag, h.Size = 0644, tar.TypeReg, int64(len(tc.data))
			h.Uid, h.Gid = os.Geteuid(), os.Getegid()
			h.ModTime = time.Unix(1600000000, 123456789)
			var archive bytes.Buffer
			tw := tar.NewWriter(&archive)
			if err := tw.WriteHeader(&h); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(tc.data); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			unpack := filepath.Join(root, "unpack")
			if err := os.Mkdir(unpack, 0755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("tar", "--extract", "--same-permissions", "--directory", unpack)
			cmd.Stdin = bytes.NewReader(archive.Bytes())
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			images := []string{filepath.Join(root, "baseline.ext4"), filepath.Join(root, "stream.ext4")}
			if err := DirectoryToImageAutoSize(t.Context(), unpack, images[0]); err != nil {
				t.Fatal(err)
			}
			if err := TarToImageAutoSize(t.Context(), func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(archive.Bytes())), nil }, images[1], false); err != nil {
				t.Fatal(err)
			}
			debug := func(image, command string) string {
				out, err := exec.Command("/sbin/debugfs", "-R", command, image).CombinedOutput()
				if err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				return string(out)
			}
			var stats []string
			for i, image := range images {
				stats = append(stats, debug(image, "stat "+h.Name))
				dest := filepath.Join(root, "dump"+strconv.Itoa(i))
				debug(image, "dump "+h.Name+" "+dest)
				got, err := os.ReadFile(dest)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, tc.data) {
					t.Fatal("payload mismatch")
				}
				attrs := debug(image, "ea_list "+h.Name)
				if strings.Contains(attrs, "user.test") || strings.Contains(attrs, "security.capability") {
					t.Fatalf("archive xattrs retained: %s", attrs)
				}
				if tc.name == "sparse" {
					match := regexp.MustCompile(`Blockcount:\s+(\d+)`).FindStringSubmatch(stats[i])
					if len(match) != 2 {
						t.Fatalf("missing block count: %s", stats[i])
					}
					blocks, err := strconv.Atoi(match[1])
					if err != nil || blocks > 32 {
						t.Fatalf("zero ranges allocated: %s", stats[i])
					}
				}
				if tc.name == "fractional_mtime" && !strings.Contains(stats[i], "mtime: 0x5f5e1000:00000000") {
					t.Fatalf("mtime not truncated to archive seconds: %s", stats[i])
				}
				out, err := exec.Command("/sbin/e2fsck", "-fn", image).CombinedOutput()
				if err != nil {
					t.Fatalf("filesystem check: %v: %s", err, out)
				}
			}
			for _, pattern := range []string{`Type:\s+\w+`, `Mode:\s+[0-7]+`, `User:\s+\d+\s+Group:\s+\d+`, `Size:\s+\d+`, `mtime:.*`} {
				re := regexp.MustCompile(pattern)
				a, b := re.FindString(stats[0]), re.FindString(stats[1])
				if a == "" || a != b {
					t.Fatalf("%s differs:\n%s\n%s", pattern, stats[0], stats[1])
				}
			}
		})
	}
}
