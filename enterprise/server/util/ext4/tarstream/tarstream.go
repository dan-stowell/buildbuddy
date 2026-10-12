// Package tarstream prepares flattened OCI tar streams for mke2fs's libarchive
// reader. It retains metadata only; file contents never go to a temporary file.
package tarstream

import (
	"archive/tar"
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const blockSize int64 = 4096

// Open must return the same flattened archive on each call. A second pass is
// necessary because mke2fs needs the filesystem size before reading stdin.
type Open func() (io.ReadCloser, error)

// Estimate conservatively counts file blocks, metadata and implicit parents.
// Hardlink data is not counted twice. Memory use is proportional to path count,
// rather than payload size. The estimate is deliberately not DiskSizeBytes:
// that function counts hardlinked contents twice and depends on an unpack tree.
func Estimate(ctx context.Context, r io.Reader, excludeDev bool) (size int64, entries int64, err error) {
	return estimate(ctx, r, excludeDev, false)
}

// EstimateCompatible rejects non-contiguous directory subtrees. GNU tar may
// restore directory metadata before a later subtree revisit, so new entries
// reset that directory's mtime to extraction time. A tar writer that restores
// archive metadata at the end cannot reproduce this behavior. Reject these
// archives before creating an image, rather than silently changing guest mtimes.
// This check is conservative: even a revisit that would not modify the parent
// directory is rejected.
func EstimateCompatible(ctx context.Context, r io.Reader, excludeDev bool) (int64, int64, error) {
	return estimate(ctx, r, excludeDev, true)
}

func estimate(ctx context.Context, r io.Reader, excludeDev, requireCompatible bool) (int64, int64, error) {
	tr := tar.NewReader(r)
	var directoryStack []string
	closed := make(map[string]bool)
	seen := map[string]bool{".": true}
	total := int64(2 * blockSize)
	for {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, err
		}
		name, err := clean(h.Name)
		if err != nil {
			return 0, 0, err
		}
		if excluded(name, excludeDev) {
			continue
		}
		if requireCompatible {
			for len(directoryStack) > 0 {
				dir := directoryStack[len(directoryStack)-1]
				if strings.HasPrefix(name, dir+"/") {
					break
				}
				closed[dir] = true
				directoryStack = directoryStack[:len(directoryStack)-1]
			}
			for p := path.Dir(name); p != "."; p = path.Dir(p) {
				if closed[p] {
					return 0, 0, fmt.Errorf("streaming ext4 cannot preserve GNU tar directory mtimes: non-contiguous directory %q", p)
				}
			}
			if h.Typeflag == tar.TypeDir && name != "." {
				closed[name] = false
				directoryStack = append(directoryStack, name)
			}
		}
		for p := name; p != "." && !seen[p]; p = path.Dir(p) {
			seen[p] = true
			total += 2 * blockSize
		}
		if h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA || h.Typeflag == tar.TypeGNUSparse {
			if h.Size > math.MaxInt64/2-total-blockSize {
				return 0, 0, fmt.Errorf("tar size overflow")
			}
			total += ((h.Size + blockSize - 1) / blockSize) * blockSize
		}
	}
	// Drain the underlying stream so errors delivered after tar EOF (including
	// mutate.Extract errors) are not mistaken for a successful conversion.
	if _, err := io.Copy(io.Discard, r); err != nil {
		return 0, 0, err
	}
	if total > math.MaxInt64/2 {
		return 0, 0, fmt.Errorf("tar size overflow")
	}
	return total + total/5 + 1024*blockSize, int64(len(seen)), nil
}

func clean(name string) (string, error) {
	// GNU tar strips leading slashes by default. Match that behavior for OCI
	// producers (and testregistry) that use absolute archive names.
	name = strings.TrimLeft(name, "/")
	if slices.Contains(strings.Split(name, "/"), "..") {
		return "", fmt.Errorf("escaping tar path %q", name)
	}
	name = path.Clean(name)
	if name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("escaping tar path %q", name)
	}
	return name, nil
}

func excluded(name string, excludeDev bool) bool {
	return excludeDev && strings.HasPrefix(name, "dev/")
}

// Normalize creates missing parents and defers hardlinks until their targets
// exist. Final directory headers restore archive metadata after children have
// been added. GNU tar without --xattrs (the current baseline) ignores archive
// xattrs, so strip those here too. Only use this with an already flattened tar.
func Normalize(ctx context.Context, r io.Reader, w io.Writer, excludeDev bool) error {
	// Read Linux's umask without changing process-global state.
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fmt.Errorf("read process umask: %w", err)
	}
	var parentMode int64
	found := false
	for line := range strings.SplitSeq(string(status), "\n") {
		if value, ok := strings.CutPrefix(line, "Umask:"); ok {
			mask, err := strconv.ParseInt(strings.TrimSpace(value), 8, 32)
			if err != nil {
				return fmt.Errorf("parse process umask: %w", err)
			}
			parentMode, found = 0777&^mask, true
			break
		}
	}
	if !found {
		return fmt.Errorf("process status missing umask")
	}
	bw := bufio.NewWriterSize(w, 256*1024)
	tr, tw := tar.NewReader(r), tar.NewWriter(bw)
	created := map[string]bool{".": true}
	dirs := make(map[string]*tar.Header)
	// mke2fs reapplies a hardlink header's metadata to the target inode. GNU
	// tar keeps the target metadata, even when the link header differs.
	metadata := make(map[string]inodeMetadata)
	var links []*tar.Header
	var parents func(string) error
	parents = func(name string) error {
		if created[name] {
			return nil
		}
		if err := parents(path.Dir(name)); err != nil {
			return err
		}
		h := &tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: parentMode, ModTime: time.Now(), Format: tar.FormatPAX}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		created[name] = true
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		h.Name, err = clean(h.Name)
		if err != nil {
			return err
		}
		// DirectoryToImage keeps mke2fs's generated root inode metadata; it
		// copies metadata only for entries under the source directory.
		if h.Name == "." && h.Typeflag == tar.TypeDir {
			continue
		}
		if excluded(h.Name, excludeDev) {
			continue
		}
		// archive/tar expands GNU sparse files while reading. Emit the logical
		// bytes as a regular file; mke2fs turns zero blocks back into holes.
		if h.Typeflag == tar.TypeGNUSparse {
			h.Typeflag = tar.TypeReg
		}
		// Linux creates symlinks with 0777 regardless of the archive mode.
		if h.Typeflag == tar.TypeSymlink {
			h.Mode = 0777
		}
		h.Xattrs = nil
		for k := range h.PAXRecords {
			if strings.HasPrefix(k, "SCHILY.xattr.") || strings.HasPrefix(k, "LIBARCHIVE.xattr.") {
				delete(h.PAXRecords, k)
			}
		}
		if err := parents(path.Dir(h.Name)); err != nil {
			return err
		}
		if h.Typeflag == tar.TypeLink {
			h.Linkname, err = clean(h.Linkname)
			if err != nil {
				return err
			}
			links = append(links, h)
			continue
		}
		if h.Typeflag == tar.TypeDir {
			dirs[h.Name] = h
			if err := parents(h.Name); err != nil {
				return err
			}
			continue
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.CopyN(tw, tr, h.Size); err != nil {
			return err
		}
		created[h.Name] = true
		metadata[h.Name] = inodeMetadata{h.Mode, h.Uid, h.Gid, h.ModTime, h.AccessTime, h.ChangeTime}
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		return err
	}
	for len(links) > 0 {
		var pending []*tar.Header
		for _, h := range links {
			if !created[h.Linkname] {
				pending = append(pending, h)
				continue
			}
			m, ok := metadata[h.Linkname]
			if !ok {
				return fmt.Errorf("unsupported hardlink target %q", h.Linkname)
			}
			h.Mode, h.Uid, h.Gid, h.ModTime, h.AccessTime, h.ChangeTime = m.mode, m.uid, m.gid, m.mtime, m.atime, m.ctime
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			created[h.Name] = true
			metadata[h.Name] = m
		}
		if len(pending) == len(links) {
			return fmt.Errorf("unresolved tar hardlink %q -> %q", pending[0].Name, pending[0].Linkname)
		}
		links = pending
	}
	names := make([]string, 0, len(dirs))
	for name := range dirs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := tw.WriteHeader(dirs[name]); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return bw.Flush()
}

type inodeMetadata struct {
	mode                int64
	uid, gid            int
	mtime, atime, ctime time.Time
}
