// A standalone module keeps the research-only hcsshim dependency out of the
// executor. Run `go build -o /tmp/oci-ext4-benchmark .` in this directory.
package main

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Microsoft/hcsshim/ext4/tar2ext4"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/ext4/tarstream"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

type counters map[string]int64

func readIO() counters {
	b, err := os.ReadFile("/proc/self/io")
	check(err)
	c := counters{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			c[strings.TrimSuffix(f[0], ":")], _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	return c
}
func subtract(a, b counters) counters {
	c := counters{}
	for k, v := range a {
		c[k] = v - b[k]
	}
	return c
}

type phase struct {
	Name          string   `json:"phase"`
	Seconds       float64  `json:"seconds"`
	IO            counters `json:"io"`
	BlocksRead    int64    `json:"rusage_inblock"`
	BlocksWritten int64    `json:"rusage_oublock"`
}
type result struct {
	Image, Digest, Variant string
	Run                    int
	Phases                 []phase
	Total                  phase
	Apparent, Allocated    int64
	Error                  string `json:"error,omitempty"`
}

func measure(label string, fn func() error) (phase, error) {
	before := readIO()
	beforeIn, beforeOut := blocks()
	start := time.Now()
	err := fn()
	elapsed := time.Since(start).Seconds()
	// Include children after Wait; Linux /proc/self/io includes waited-child I/O.
	// Do not sync between phases: deleting the dirty unpack tree can cancel
	// writes, and forced writeback would hide that behavior.
	afterIn, afterOut := blocks()
	return phase{Name: label, Seconds: elapsed, IO: subtract(readIO(), before), BlocksRead: afterIn - beforeIn, BlocksWritten: afterOut - beforeOut}, err
}
func blocks() (int64, int64) {
	var self, children syscall.Rusage
	check(syscall.Getrusage(syscall.RUSAGE_SELF, &self))
	check(syscall.Getrusage(syscall.RUSAGE_CHILDREN, &children))
	return self.Inblock + children.Inblock, self.Oublock + children.Oublock
}
func check(err error) {
	if err != nil {
		panic(err)
	}
}
func command(stdin io.Reader, bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = stdin
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", bin, err, b)
	}
	return nil
}
func dropCaches() error {
	syscall.Sync()
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0644)
}
func estimateDir(dir string) (int64, error) {
	var total int64
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		total += 4096
		if info.Mode().IsRegular() {
			total += ((info.Size() + 4095) / 4096) * 4096
		} else if info.IsDir() {
			total += 4096
		}
		return nil
	})
	return total + total/5 + 4*1024*1024, err
}
func mkfs(bin, src, out string, size, entries int64, stdin io.Reader) error {
	return command(stdin, bin, "-L", "''", "-N", fmt.Sprint(entries), "-O", "^64bit", "-d", src, "-m", "5", "-r", "1", "-b", "4096", "-t", "ext4", out, fmt.Sprintf("%dK", size/1024))
}
func main() {
	image := flag.String("image", "mirror.gcr.io/library/alpine:3.20", "registry image")
	work := flag.String("work", "/tmp/oci-ext4-bench", "local OCI layer cache and results")
	variants := flag.String("variants", "baseline,mke2fs,hcsshim", "comma separated; also tmpfs, mke2fs-raw, mke2fs-strict (executor path) and hcsshim-normalized")
	runs := flag.Int("runs", 3, "cold-cache runs per variant")
	mke := flag.String("mke2fs", "/sbin/mke2fs", "mke2fs >=1.47.1 with libarchive")
	tarSource := flag.String("tar", "", "use a local tar fixture instead of registry layers")
	tmpfsRoot := flag.String("tmpfs-root", "/dev/shm", "mounted tmpfs directory (operator sets its memory limit)")
	fetchOnly := flag.Bool("fetch-only", false, "populate layer cache only")
	finalize := flag.String("finalize", "filecache", "filecache (hardlink), legacy (hash), or none")
	flag.Parse()
	if *tarSource != "" {
		*image = "fixture:" + *tarSource
	}
	check(os.MkdirAll(*work, 0755))
	ctx := context.Background()
	var img v1.Image
	lp, err := layout.FromPath(filepath.Join(*work, "oci"))
	if *tarSource != "" {
		layer, e := tarball.LayerFromFile(*tarSource)
		check(e)
		img, e = mutate.AppendLayers(empty.Image, layer)
		check(e)
	} else if err == nil {
		idx, e := lp.ImageIndex()
		check(e)
		m, e := idx.IndexManifest()
		check(e)
		img, e = lp.Image(m.Manifests[0].Digest)
		check(e)
	} else {
		p, e := measure("fetch", func() error {
			ref, e := name.ParseReference(*image)
			if e != nil {
				return e
			}
			img, e = remote.Image(ref, remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"}))
			if e != nil {
				return e
			}
			lp, e = layout.Write(filepath.Join(*work, "oci"), mutate.IndexMediaType(empty.Index, types.OCIImageIndex))
			if e != nil {
				return e
			}
			return lp.AppendImage(img)
		})
		check(e)
		check(json.NewEncoder(os.Stdout).Encode(p))
		// Reopen from disk, so timed conversion never fetches registry layers.
		idx, e := lp.ImageIndex()
		check(e)
		m, e := idx.IndexManifest()
		check(e)
		img, e = lp.Image(m.Manifests[0].Digest)
		check(e)
	}
	digest, err := img.Digest()
	check(err)
	layers, err := img.Layers()
	check(err)
	var logical int64
	for _, l := range layers {
		s, e := l.Size()
		check(e)
		logical += s
	}
	fmt.Fprintf(os.Stderr, "image=%s digest=%s compressed_layers=%d\n", *image, digest, logical)
	if *fetchOnly {
		return
	}
	// Record the flattened payload size to establish the large-image workload.
	// This untimed inspection is followed by sync+drop_caches before every run.
	rc := mutate.Extract(img)
	tr := tar.NewReader(rc)
	var payload, paths int64
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		check(e)
		paths++
		if h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA {
			payload += h.Size
		}
	}
	_, err = io.Copy(io.Discard, rc)
	check(err)
	check(rc.Close())
	fmt.Fprintf(os.Stderr, "flattened_payload=%d entries=%d\n", payload, paths)

	for _, variant := range strings.Split(*variants, ",") {
		for run := 1; run <= *runs; run++ {
			check(dropCaches())
			out := filepath.Join(*work, variant+".ext4")
			check(os.RemoveAll(out))
			res := result{Image: *image, Digest: digest.String(), Variant: variant, Run: run}
			before := readIO()
			beforeIn, beforeOut := blocks()
			start := time.Now()
			add := func(label string, fn func() error) error {
				p, e := measure(label, fn)
				res.Phases = append(res.Phases, p)
				return e
			}
			var size, entries int64
			var conversionErr error
			switch variant {
			case "baseline", "tmpfs":
				dir := filepath.Join(*work, "unpack")
				if variant == "tmpfs" {
					dir = filepath.Join(*tmpfsRoot, "oci-ext4-unpack")
				}
				check(os.RemoveAll(dir))
				check(os.MkdirAll(dir, 0755))
				conversionErr = add("extract", func() error {
					rc := mutate.Extract(img)
					defer rc.Close()
					return command(rc, "tar", "--extract", "--directory", dir)
				})
				if conversionErr == nil {
					conversionErr = add("size", func() error { var e error; size, e = estimateDir(dir); return e })
				}
				if conversionErr == nil {
					conversionErr = add("populate", func() error { return mkfs(*mke, dir, out, size, 0, nil) })
				}
				e := add("cleanup", func() error { return os.RemoveAll(dir) })
				if conversionErr == nil {
					conversionErr = e
				}
			case "mke2fs", "mke2fs-raw", "mke2fs-strict":
				conversionErr = add("size-pass", func() error {
					rc := mutate.Extract(img)
					defer rc.Close()
					var e error
					if variant == "mke2fs-strict" {
						size, entries, e = tarstream.EstimateCompatible(ctx, rc, false)
					} else {
						size, entries, e = tarstream.Estimate(ctx, rc, false)
					}
					return e
				})
				if conversionErr == nil {
					conversionErr = add("populate", func() error {
						rc := mutate.Extract(img)
						defer rc.Close()
						if variant == "mke2fs-raw" {
							return mkfs(*mke, "-", out, size, entries+11, rc)
						}
						pr, pw, e := os.Pipe()
						if e != nil {
							return e
						}
						defer pr.Close()
						done := make(chan error, 1)
						go func() { e := tarstream.Normalize(ctx, rc, pw, false); pw.Close(); done <- e }()
						e = mkfs(*mke, "-", out, size, entries+11, pr)
						pr.Close()
						rc.Close()
						streamErr := <-done
						if e != nil {
							return e
						}
						return streamErr
					})
				}
			case "hcsshim", "hcsshim-normalized":
				conversionErr = add("populate", func() error {
					rc := mutate.Extract(img)
					defer rc.Close()
					f, e := os.Create(out)
					if e != nil {
						return e
					}
					defer f.Close()
					if variant == "hcsshim" {
						return tar2ext4.ConvertTarToExt4(rc, f)
					}
					pr, pw := io.Pipe()
					done := make(chan error, 1)
					go func() { e := tarstream.Normalize(ctx, rc, pw, false); pw.CloseWithError(e); done <- e }()
					e = tar2ext4.ConvertTarToExt4(pr, f)
					pr.Close()
					rc.Close()
					streamErr := <-done
					if e != nil {
						return e
					}
					return streamErr
				})
			default:
				panic("unknown variant " + variant)
			}
			if conversionErr == nil {
				conversionErr = add("finalize", func() error {
					switch *finalize {
					case "filecache":
						cache := out + ".cache"
						os.Remove(cache)
						e := os.Link(out, cache)
						if e != nil {
							return e
						}
						return os.Remove(cache)
					case "legacy":
						f, e := os.Open(out)
						if e != nil {
							return e
						}
						defer f.Close()
						_, e = io.Copy(sha256.New(), f)
						return e
					case "none":
						return nil
					default:
						return fmt.Errorf("unknown finalize %q", *finalize)
					}
				})
			}
			afterIn, afterOut := blocks()
			res.Total = phase{Name: "total", Seconds: time.Since(start).Seconds(), IO: subtract(readIO(), before), BlocksRead: afterIn - beforeIn, BlocksWritten: afterOut - beforeOut}
			// Flush only after timing and I/O counters have been captured, for stable
			// st_blocks/du results. The next run begins with another sync+cache drop.
			syscall.Sync()
			if conversionErr != nil {
				res.Error = conversionErr.Error()
			} else {
				info, e := os.Stat(out)
				check(e)
				res.Apparent = info.Size()
				res.Allocated = info.Sys().(*syscall.Stat_t).Blocks * 512
			}
			check(json.NewEncoder(os.Stdout).Encode(res))
		}
	}
}
