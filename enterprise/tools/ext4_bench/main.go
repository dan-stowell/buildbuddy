//go:build linux

// ext4_bench measures the unpack-directory conversion pipeline on Linux.
// Run as root. Fetch once with -fetch; benchmark the flattened tar with -tar.
package main

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sys/unix"
)

// /proc/self/io includes children after wait(2). Snapshot after cmd.Run, not
// while a child is alive. cancelled_write_bytes is reported separately since
// unlinking the unpack directory can cancel earlier dirty-page writes.
type counters struct {
	Read      int64 `json:"read_bytes"`
	Write     int64 `json:"write_bytes"`
	Cancelled int64 `json:"cancelled_write_bytes"`
	Net       int64 `json:"net_write_bytes"`
}

func procIO() counters {
	b, err := os.ReadFile("/proc/self/io")
	must(err)
	var c counters
	for l := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		n, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "read_bytes:":
			c.Read = n
		case "write_bytes:":
			c.Write = n
		case "cancelled_write_bytes:":
			c.Cancelled = n
		}
	}
	c.Net = c.Write - c.Cancelled
	return c
}
func subtract(a, b counters) counters {
	return counters{a.Read - b.Read, a.Write - b.Write, a.Cancelled - b.Cancelled, a.Net - b.Net}
}

type phase struct {
	Name    string   `json:"name"`
	Seconds float64  `json:"seconds"`
	IO      counters `json:"io"`
}

func measure(n string, f func()) phase {
	before := procIO()
	t := time.Now()
	f()
	return phase{n, time.Since(t).Seconds(), subtract(procIO(), before)}
}
func must(e error) {
	if e != nil {
		panic(e)
	}
}
func run(n string, args ...string) {
	c := exec.Command(n, args...)
	if n == "/sbin/mke2fs" {
		c.Env = append(os.Environ(), "E2FSPROGS_FAKE_TIME=1700000000")
	}
	if b, e := c.CombinedOutput(); e != nil {
		panic(fmt.Sprintf("%s %v: %v: %s", n, args, e, b))
	}
}
func statSize(p string) (int64, int64) {
	s, e := os.Stat(p)
	must(e)
	return s.Size(), s.Sys().(*syscall.Stat_t).Blocks * 512
}
func estimate(dir string) (int64, int64) {
	var size, n int64
	must(filepath.WalkDir(dir, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		s, e := os.Lstat(p)
		if e != nil {
			return e
		}
		n++
		size += 4096
		if s.Mode().IsRegular() {
			size += (s.Size() + 4095) / 4096 * 4096
		} else if s.IsDir() {
			size += 4096
		}
		return nil
	}))
	return size, n
}

// Mirror ConvertFileToCOW's SEEK_DATA/SEEK_HOLE loop, without the application
// cache setup. This isolates the disk IO of chunk creation (4 MiB chunks).
func chunks(src, dir string) {
	must(os.MkdirAll(dir, 0755))
	f, e := os.Open(src)
	must(e)
	defer f.Close()
	size, _ := statSize(src)
	const chunkSize int64 = 4 << 20
	for start := int64(0); start < size; start += chunkSize {
		end := min(start+chunkSize, size)
		data, e := syscall.Seek(int(f.Fd()), start, 3)
		if e == syscall.ENXIO {
			break
		}
		must(e)
		if data >= end {
			continue
		}
		out, e := os.Create(filepath.Join(dir, strconv.FormatInt(start, 10)))
		must(e)
		must(out.Truncate(end - start))
		for data < end {
			hole, e := syscall.Seek(int(f.Fd()), data, 4)
			must(e)
			_, e = f.Seek(data, 0)
			must(e)
			_, e = out.Seek(data-start, 0)
			must(e)
			_, e = io.CopyN(out, f, min(hole, end)-data)
			must(e)
			data, e = syscall.Seek(int(f.Fd()), hole, 3)
			if e == syscall.ENXIO {
				break
			}
			must(e)
		}
		must(out.Close())
	}
}

type entry struct {
	Mode                 uint32
	UID, GID             uint32
	Size                 int64
	Mtime                int64
	MtimeNS              int64
	Rdev                 uint64
	Link, Hash, Hardlink string
	Xattrs               map[string]string
}

func manifest(root string) map[string]entry {
	result := map[string]entry{}
	links := map[uint64]string{}
	must(filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		s, e := os.Lstat(p)
		if e != nil {
			return e
		}
		st := s.Sys().(*syscall.Stat_t)
		v := entry{Mode: st.Mode, UID: st.Uid, GID: st.Gid, Mtime: st.Mtim.Sec, MtimeNS: st.Mtim.Nsec, Rdev: st.Rdev}
		if s.Mode().IsRegular() {
			v.Size = s.Size()
			f, e := os.Open(p)
			if e != nil {
				return e
			}
			h := sha256.New()
			_, e = io.Copy(h, f)
			f.Close()
			if e != nil {
				return e
			}
			v.Hash = hex.EncodeToString(h.Sum(nil))
			if st.Nlink > 1 {
				first, ok := links[st.Ino]
				if !ok {
					first = rel
					links[st.Ino] = first
				}
				v.Hardlink = first
			}
		}
		if s.Mode()&os.ModeSymlink != 0 {
			v.Link, e = os.Readlink(p)
			if e != nil {
				return e
			}
		}
		{
			n, e := unix.Llistxattr(p, nil)
			if e != nil {
				return e
			}
			b := make([]byte, n)
			_, e = unix.Llistxattr(p, b)
			if e != nil {
				return e
			}
			v.Xattrs = map[string]string{}
			for k := range strings.SplitSeq(string(b), "\x00") {
				if k == "" {
					continue
				}
				n, e := unix.Lgetxattr(p, k, nil)
				if e != nil {
					return e
				}
				b := make([]byte, n)
				_, e = unix.Lgetxattr(p, k, b)
				if e != nil {
					return e
				}
				v.Xattrs[k] = hex.EncodeToString(b)
			}
		}
		result[rel] = v
		return nil
	}))
	return result
}
func verify(image, base, root string) {
	mount := filepath.Join(root, "mount")
	must(os.MkdirAll(mount, 0755))
	run("e2fsck", "-fn", image)
	run("mount", "-o", "loop,ro", image, mount)
	defer run("umount", mount)
	b, e := json.MarshalIndent(manifest(mount), "", "  ")
	must(e)
	path := base + ".tree.json"
	if image == base {
		must(os.WriteFile(path, b, 0644))
		return
	}
	expected, e := os.ReadFile(path)
	must(e)
	if string(expected) != string(b) {
		must(os.WriteFile(image+".tree.json", b, 0644))
		panic("tree mismatch: diff " + path + " " + image + ".tree.json")
	}
}
func main() {
	compare := flag.String("compare", "", "comma-separated image paths to mount, fsck, and compare instead of benchmarking")
	fetch := flag.String("fetch", "", "OCI image to flatten using mutate.Extract (outside timed runs)")
	tarPath := flag.String("tar", "", "flattened rootfs tar")
	root := flag.String("work", "/var/tmp/ext4-bench", "disk workspace")
	label := flag.String("image", "", "image label")
	configs := flag.String("configs", "baseline,lazy,nodiscard,lazy-nodiscard,small-journal,no-journal,compact,inodes,tmpfs,prezeroed,tmpfs-prezeroed", "comma-separated configs")
	repeats := flag.Int("runs", 3, "cold-cache repetitions")
	check := flag.Bool("verify", true, "mount and compare every tree, run e2fsck")
	tmpfsBytes := flag.Int64("tmpfs-bytes", 2<<30, "private tmpfs size limit for tmpfs variants")
	retain := flag.Bool("retain", false, "retain all output images")
	flag.Parse()
	must(os.MkdirAll(*root, 0755))
	if *compare != "" {
		images := strings.Split(*compare, ",")
		if len(images) < 2 {
			panic("-compare requires at least two images")
		}
		for _, image := range images {
			verify(image, images[0], *root)
		}
		fmt.Println("image trees match (contents, modes, uid/gid, links, devices, xattrs, mtimes)")
		return
	}
	if *fetch != "" {
		ref, e := name.ParseReference(*fetch)
		must(e)
		img, e := remote.Image(ref, remote.WithContext(context.Background()), remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"}))
		must(e)
		r := mutate.Extract(img)
		defer r.Close()
		f, e := os.Create(*tarPath)
		must(e)
		_, e = io.Copy(f, r)
		must(e)
		must(f.Close())
		digest, e := img.Digest()
		must(e)
		fmt.Fprintf(os.Stderr, "%s@%s\n", *fetch, digest)
		return
	}
	if *tarPath == "" {
		panic("-tar is required")
	}
	// GNU tar can change previously restored directory mtimes when a flattened
	// tar revisits them. Freeze directory fixture timestamps between runs while
	// preserving all archive file/symlink timestamps. This is benchmark-only.
	directoryTimes := map[string]time.Time{".": time.Unix(1700000000, 0)}
	archive, err := os.Open(*tarPath)
	must(err)
	tr := tar.NewReader(archive)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		must(err)
		if h.Typeflag == tar.TypeDir {
			directoryTimes[filepath.Clean(h.Name)] = h.ModTime
		}
	}
	must(archive.Close())
	base := filepath.Join(*root, "baseline-1.ext4")
	for config := range strings.SplitSeq(*configs, ",") {
		for iteration := 1; iteration <= *repeats; iteration++ {
			run("sync")
			must(os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0600))
			dir, e := os.MkdirTemp(*root, "unpack-*")
			must(e)
			tmpfs := config == "tmpfs" || config == "tmpfs-prezeroed"
			if tmpfs {
				must(unix.Mount("tmpfs", dir, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, fmt.Sprintf("size=%d,mode=0700", *tmpfsBytes)))
			}
			image := filepath.Join(*root, fmt.Sprintf("%s-%d.ext4", config, iteration))
			must(os.RemoveAll(image))
			before := procIO()
			started := time.Now()
			phases := []phase{}
			phases = append(phases, measure("unpack", func() {
				args := []string{"--extract", "--file", *tarPath, "--directory", dir}
				if config == "tar-no-same" {
					args = append(args, "--no-same-owner", "--no-same-permissions")
				}
				run("tar", args...)
				must(filepath.WalkDir(dir, func(p string, d fs.DirEntry, e error) error {
					if e != nil {
						return e
					}
					if !d.IsDir() {
						return nil
					}
					rel, e := filepath.Rel(dir, p)
					if e != nil {
						return e
					}
					t, ok := directoryTimes[rel]
					if !ok {
						t = time.Unix(1700000000, 0)
					}
					return os.Chtimes(p, t, t)
				}))
			}))
			var estimateBytes, inodes int64
			phases = append(phases, measure("estimate", func() { estimateBytes, inodes = estimate(dir) }))
			size := int64(float64(estimateBytes)*1.2) + (4 << 20)
			if config == "compact" || config == "inodes" {
				size = estimateBytes + (4 << 20)
			}
			args := []string{"-L", "''", "-N", "0", "-O", "^64bit", "-d", dir, "-m", "5", "-r", "1", "-b", "4096", "-t", "ext4"}
			switch config {
			case "lazy":
				args = append(args, "-E", "lazy_itable_init=1,lazy_journal_init=1")
			case "nodiscard":
				args = append(args, "-E", "nodiscard")
			case "lazy-nodiscard", "compact", "inodes":
				args = append(args, "-E", "lazy_itable_init=1,lazy_journal_init=1,nodiscard")
			case "small-journal":
				args = append(args, "-E", "lazy_itable_init=1,lazy_journal_init=1,nodiscard", "-J", "size=4")
			case "no-journal":
				args = append(args, "-E", "lazy_itable_init=1,nodiscard", "-O", "^64bit,^has_journal")
			case "prezeroed", "tmpfs-prezeroed":
				args = append(args, "-E", "assume_storage_prezeroed=1,nodiscard")
			case "baseline", "tmpfs", "tar-no-same", "no-fsync":
			default:
				panic("unknown config " + config)
			}
			if config == "inodes" {
				args = append(args, "-N", strconv.FormatInt(inodes+max(inodes/10, 128), 10))
			}
			args = append(args, image, fmt.Sprintf("%dK", size/1024))
			phases = append(phases, measure("mke2fs", func() {
				if config == "no-fsync" {
					_, e := os.Stat(filepath.Join(*root, "no_fsync.so"))
					must(e)
					c := exec.Command("/sbin/mke2fs", args...)
					c.Env = append(os.Environ(), "E2FSPROGS_FAKE_TIME=1700000000", "LD_PRELOAD="+filepath.Join(*root, "no_fsync.so"))
					if b, e := c.CombinedOutput(); e != nil {
						panic(fmt.Sprintf("mke2fs: %v: %s", e, b))
					}
				} else {
					run("/sbin/mke2fs", args...)
				}
			}))
			cached := image + ".cache"
			phases = append(phases, measure("filecache-hardlink", func() { must(os.Link(image, cached)) }))
			apparent, allocated := statSize(image)
			_, cacheAllocated := statSize(cached)
			phases = append(phases, measure("cleanup", func() {
				if tmpfs {
					must(unix.Unmount(dir, 0))
				}
				must(os.RemoveAll(dir))
				must(os.Remove(cached))
			}))
			phases = append(phases, measure("conversion-sync", func() { run("sync") }))
			conversion := phase{"conversion-total", time.Since(started).Seconds(), subtract(procIO(), before)}
			chunkDir := image + ".chunks"
			phases = append(phases, measure("chunk", func() { chunks(image, chunkDir) }))
			var chunkAllocated int64
			must(filepath.WalkDir(chunkDir, func(p string, d fs.DirEntry, e error) error {
				if e != nil {
					return e
				}
				if !d.IsDir() {
					_, a := statSize(p)
					chunkAllocated += a
				}
				return nil
			}))
			phases = append(phases, measure("chunk-sync", func() { run("sync") }))
			total := phase{"total", time.Since(started).Seconds(), subtract(procIO(), before)}
			record := struct {
				Image, Config                                                          string
				Run                                                                    int
				Estimate, Entries, Apparent, Allocated, CacheAllocated, ChunkAllocated int64
				Phases                                                                 []phase
				Conversion, Total                                                      phase
			}{*label, config, iteration, estimateBytes, inodes, apparent, allocated, cacheAllocated, chunkAllocated, phases, conversion, total}
			b, e := json.Marshal(record)
			must(e)
			fmt.Println(string(b))
			if *check {
				verify(image, base, *root)
			}
			must(os.RemoveAll(chunkDir))
			if !*retain && image != base {
				must(os.Remove(image))
			}
		}
	}
}
