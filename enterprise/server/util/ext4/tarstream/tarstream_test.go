package tarstream

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func archive(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestNormalize(t *testing.T) {
	mtime := time.Unix(123456789, 0)
	data := archive(t,
		&tar.Header{Name: "a/hard", Typeflag: tar.TypeLink, Linkname: "b/target", Mode: 0640},
		&tar.Header{Name: "b/target", Typeflag: tar.TypeReg, Size: 3, Mode: 0640, Uid: 12, Gid: 34, ModTime: mtime, Xattrs: map[string]string{"security.capability": "ignored"}},
		&tar.Header{Name: "b", Typeflag: tar.TypeDir, Mode: 0700, Uid: 12, ModTime: mtime},
		&tar.Header{Name: "sym", Typeflag: tar.TypeSymlink, Linkname: "../absolute/target", Mode: 0777},
		&tar.Header{Name: "dev/null", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3},
	)
	var out bytes.Buffer
	if err := Normalize(context.Background(), bytes.NewReader(data), &out, true); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&out)
	seen := map[string]*tar.Header{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeLink {
			if seen[h.Linkname] == nil {
				t.Fatal("link before target")
			}
			if h.Uid != 12 || h.Gid != 34 || !h.ModTime.Equal(mtime) {
				t.Fatalf("hardlink changed target metadata: %+v", h)
			}
		}
		if h.Name == "b/target" {
			payload, _ := io.ReadAll(tr)
			if string(payload) != "xxx" || h.Uid != 12 || h.Gid != 34 || len(h.Xattrs) != 0 || !h.ModTime.Equal(mtime) {
				t.Fatalf("bad target: %+v %q", h, payload)
			}
		}
		seen[h.Name] = h
	}
	if seen["a"] == nil || seen["b"].Mode != 0700 || !seen["b"].ModTime.Equal(mtime) || seen["dev/null"] != nil || seen["sym"].Linkname != "../absolute/target" {
		t.Fatalf("bad tree: %+v", seen)
	}
}
func TestEstimate(t *testing.T) {
	data := archive(t, &tar.Header{Name: "a/file", Typeflag: tar.TypeReg, Size: 4097}, &tar.Header{Name: "a/hard", Typeflag: tar.TypeLink, Linkname: "a/file"})
	size, entries, err := Estimate(context.Background(), bytes.NewReader(data), false)
	if err != nil {
		t.Fatal(err)
	}
	// Root, a, file and hardlink; two payload blocks.
	total := int64(4*8192 + 8192)
	if size != total+total/5+4194304 || entries != 4 {
		t.Fatalf("size=%d entries=%d", size, entries)
	}
}
func TestRejectPathsAndUnresolvedLinks(t *testing.T) {
	for _, name := range []string{"../escape", "a/../../escape"} {
		data := archive(t, &tar.Header{Name: name})
		if _, _, err := Estimate(context.Background(), bytes.NewReader(data), false); err == nil {
			t.Errorf("accepted %q", name)
		}
		if err := Normalize(context.Background(), bytes.NewReader(data), io.Discard, false); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	data := archive(t, &tar.Header{Name: "a", Typeflag: tar.TypeLink, Linkname: "b"}, &tar.Header{Name: "b", Typeflag: tar.TypeLink, Linkname: "a"})
	if err := Normalize(context.Background(), bytes.NewReader(data), io.Discard, false); err == nil {
		t.Fatal("accepted cyclic links")
	}
}
func TestCancelledAndTruncated(t *testing.T) {
	data := archive(t, &tar.Header{Name: "file", Size: 1024})
	for _, bad := range [][]byte{data[:100], data[:600]} {
		if _, _, err := Estimate(context.Background(), bytes.NewReader(bad), false); err == nil {
			t.Fatal("accepted truncated tar")
		}
		if err := Normalize(context.Background(), bytes.NewReader(bad), io.Discard, false); err == nil {
			t.Fatal("accepted truncated tar")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Estimate(ctx, bytes.NewReader(data), false); err == nil {
		t.Fatal("ignored cancellation")
	}
	if err := Normalize(ctx, bytes.NewReader(data), io.Discard, false); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestLeadingSlashes(t *testing.T) {
	data := archive(t, &tar.Header{Name: "/parent/file", Size: 1}, &tar.Header{Name: "/parent/link", Typeflag: tar.TypeLink, Linkname: "/parent/file"})
	var out bytes.Buffer
	if err := Normalize(context.Background(), bytes.NewReader(data), &out, false); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&out)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(h.Name, "/") || strings.HasPrefix(h.Linkname, "/") {
			t.Fatalf("absolute header: %+v", h)
		}
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, fmt.Errorf("late source error") }
func TestLateSourceError(t *testing.T) {
	data := archive(t, &tar.Header{Name: "file", Size: 3})
	if _, _, err := Estimate(context.Background(), io.MultiReader(bytes.NewReader(data), errorReader{}), false); err == nil || !strings.Contains(err.Error(), "late source error") {
		t.Fatalf("lost error: %v", err)
	}
	if err := Normalize(context.Background(), io.MultiReader(bytes.NewReader(data), errorReader{}), io.Discard, false); err == nil || !strings.Contains(err.Error(), "late source error") {
		t.Fatalf("lost error: %v", err)
	}
}
func TestRootMetadata(t *testing.T) {
	data := archive(t, &tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0700, Uid: 123, ModTime: time.Unix(123, 0)}, &tar.Header{Name: "file", Size: 1})
	var b bytes.Buffer
	if err := Normalize(context.Background(), bytes.NewReader(data), &b, false); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&b)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == "." {
			t.Fatal("overrode generated root metadata")
		}
	}
}

func TestCompatibleDirectoryOrder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		headers   []*tar.Header
		wantError bool
	}{
		{"contiguous", []*tar.Header{{Name: "a", Typeflag: tar.TypeDir}, {Name: "a/file"}, {Name: "b", Typeflag: tar.TypeDir}, {Name: "b/file"}}, false},
		{"revisited", []*tar.Header{{Name: "a", Typeflag: tar.TypeDir}, {Name: "a/file"}, {Name: "b", Typeflag: tar.TypeDir}, {Name: "a/late"}}, true},
		{"late parent header", []*tar.Header{{Name: "a/file"}, {Name: "a", Typeflag: tar.TypeDir}}, false},
		{"directory stack", []*tar.Header{{Name: "a", Typeflag: tar.TypeDir}, {Name: "a/b", Typeflag: tar.TypeDir}, {Name: "a/b/file"}, {Name: "a/file"}}, false},
		{"closed child", []*tar.Header{{Name: "a", Typeflag: tar.TypeDir}, {Name: "a/b", Typeflag: tar.TypeDir}, {Name: "a/file"}, {Name: "a/b/late"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := EstimateCompatible(context.Background(), bytes.NewReader(archive(t, tc.headers...)), false)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v wantError=%v", err, tc.wantError)
			}
		})
	}
}

func TestEstimateCompatibleRejectsHardlinksToSymlinks(t *testing.T) {
	for _, headers := range [][]*tar.Header{
		{{Name: "sym", Typeflag: tar.TypeSymlink, Linkname: "file"}, {Name: "hard", Typeflag: tar.TypeLink, Linkname: "sym"}},
		{{Name: "hard", Typeflag: tar.TypeLink, Linkname: "sym"}, {Name: "sym", Typeflag: tar.TypeSymlink, Linkname: "file"}},
		{{Name: "second", Typeflag: tar.TypeLink, Linkname: "hard"}, {Name: "hard", Typeflag: tar.TypeLink, Linkname: "sym"}, {Name: "sym", Typeflag: tar.TypeSymlink, Linkname: "file"}},
	} {
		data := archive(t, headers...)
		if _, _, err := EstimateCompatible(t.Context(), bytes.NewReader(data), false); !errors.Is(err, ErrIncompatible) {
			t.Fatalf("expected incompatible archive, got %v", err)
		}
		if _, _, err := Estimate(t.Context(), bytes.NewReader(data), false); err != nil {
			t.Fatal(err)
		}
	}
	// Excluded device entries cannot trigger a fallback.
	data := archive(t, &tar.Header{Name: "dev/sym", Typeflag: tar.TypeSymlink, Linkname: "file"}, &tar.Header{Name: "dev/hard", Typeflag: tar.TypeLink, Linkname: "dev/sym"})
	if _, _, err := EstimateCompatible(t.Context(), bytes.NewReader(data), true); err != nil {
		t.Fatal(err)
	}
}
