package ociconv

// Opt-in integration benchmark. Run the compiled test binary as root outside
// Bazel's sandbox: OCICONV_IO_BENCH=1 <binary> -test.run=TestConversionIO -test.v.
// Each sample uses a fresh cache and drops the host page cache. Never run this
// on a shared production host. See result.md for commands and interpretation.
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/clientidentity"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/oci/ocifetcher"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/copy_on_write"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/filecache"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/oci"
	ofpb "github.com/buildbuddy-io/buildbuddy/proto/oci_fetcher"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/backends/disk_cache"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/resources"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testcache"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/claims"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	ctr "github.com/google/go-containerregistry/pkg/v1"
	"github.com/stretchr/testify/require"
)

type ioSample struct {
	LogicalRead  int64 `json:"rchar"`
	LogicalWrite int64 `json:"wchar"`
	Read         int64 `json:"read_bytes"`
	Write        int64 `json:"write_bytes"`
	Cancelled    int64 `json:"cancelled_write_bytes"`
	ReadOps      int64 `json:"read_syscalls"`
	WriteOps     int64 `json:"write_syscalls"`
	InBlocks     int64 `json:"in_blocks"`
	OutBlocks    int64 `json:"out_blocks"`
}

func sampleIO(t *testing.T) ioSample {
	t.Helper()
	b, err := os.ReadFile("/proc/self/io")
	require.NoError(t, err)
	m := map[string]int64{}
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		m[strings.TrimSuffix(f[0], ":")], err = strconv.ParseInt(f[1], 10, 64)
		require.NoError(t, err)
	}
	var self, children syscall.Rusage
	require.NoError(t, syscall.Getrusage(syscall.RUSAGE_SELF, &self))
	require.NoError(t, syscall.Getrusage(syscall.RUSAGE_CHILDREN, &children))
	// Linux /proc/<tgid>/io includes all threads and waited-for children. All
	// conversion subprocesses have been waited for at our phase boundaries.
	return ioSample{m["rchar"], m["wchar"], m["read_bytes"], m["write_bytes"], m["cancelled_write_bytes"], m["syscr"], m["syscw"], self.Inblock + children.Inblock, self.Oublock + children.Oublock}
}
func (s ioSample) sub(b ioSample) ioSample {
	return ioSample{s.LogicalRead - b.LogicalRead, s.LogicalWrite - b.LogicalWrite, s.Read - b.Read, s.Write - b.Write, s.Cancelled - b.Cancelled, s.ReadOps - b.ReadOps, s.WriteOps - b.WriteOps, s.InBlocks - b.InBlocks, s.OutBlocks - b.OutBlocks}
}

type phaseResult struct {
	Phase   string   `json:"phase"`
	Seconds float64  `json:"seconds"`
	IO      ioSample `json:"io"`
}

func TestConversionIO(t *testing.T) {
	if os.Getenv("OCICONV_IO_BENCH") != "1" {
		t.Skip("opt-in real-image disk IO benchmark")
	}
	flags.Set(t, "executor.mmap_memory_bytes", int64(256*1024*1024))
	require.NoError(t, resources.Configure(true))
	require.Equal(t, 0, os.Geteuid(), "run as root to drop caches and mount images")
	images := []string{"mirror.gcr.io/library/alpine:3.20", "mirror.gcr.io/library/ubuntu:24.04", "gcr.io/flame-public/rbe-ubuntu24-04:latest"}
	if s := os.Getenv("OCICONV_IO_IMAGES"); s != "" {
		images = strings.Split(s, ",")
	}
	configs := []string{"filecache-baseline", "filecache-local-temp", "legacy-hash", "legacy-manifest"}
	if s := os.Getenv("OCICONV_IO_CONFIGS"); s != "" {
		configs = strings.Split(s, ",")
	}
	runs := 3
	if s := os.Getenv("OCICONV_IO_RUNS"); s != "" {
		var err error
		runs, err = strconv.Atoi(s)
		require.NoError(t, err)
	}
	require.GreaterOrEqual(t, runs, 1)
	for _, c := range configs {
		require.Contains(t, []string{"filecache-baseline", "filecache-local-temp", "legacy-hash", "legacy-manifest"}, c)
	}
	fetchMode := os.Getenv("OCICONV_IO_FETCH_MODE")
	if fetchMode == "" {
		fetchMode = "direct"
	}
	require.Contains(t, []string{"direct", "direct-cache", "fetcher"}, fetchMode)
	for _, image := range images {
		for _, config := range configs {
			for run := 1; run <= runs; run++ {
				t.Run(fmt.Sprintf("%s/%s/%d", image, config, run), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
					defer cancel()
					root := t.TempDir()
					te := testenv.GetTestEnv(t)
					useFetcher := fetchMode == "fetcher"
					flags.Set(t, "executor.use_oci_fetcher", useFetcher)
					flags.Set(t, "executor.container_registry.use_cache_percent", 0)
					if fetchMode != "direct" {
						// Colocate a disk-backed CAS and the fetcher in this process
						// so /proc/self/io captures both client and server IO.
						dc, err := disk_cache.NewDiskCache(te, &disk_cache.Options{RootDirectory: filepath.Join(root, "cas")}, 30_000_000_000)
						require.NoError(t, err)
						flags.Set(t, "app.client_identity.client", interfaces.ClientIdentityApp)
						flags.Set(t, "app.client_identity.key", "benchmark-test-key")
						require.NoError(t, clientidentity.Register(te))
						te.SetCache(dc)
						dc.WaitUntilMapped()
						defer dc.Stop()
						srv, runServer, lis := testenv.RegisterLocalGRPCServer(t, te)
						testcache.Setup(t, te, lis)
						if useFetcher {
							of, err := ocifetcher.NewServer(te.GetByteStreamClient(), te.GetActionCacheClient())
							require.NoError(t, err)
							ofpb.RegisterOCIFetcherServer(srv, of)
							conn, err := testenv.LocalGRPCConn(t, ctx, lis)
							require.NoError(t, err)
							defer conn.Close()
							te.SetOCIFetcherClient(ofpb.NewOCIFetcherClient(conn))
						} else {
							flags.Set(t, "executor.container_registry.use_cache_percent", 100)
							ctx = claims.AuthContext(ctx, &claims.Claims{GroupID: "benchmark"})
						}
						go runServer()
					}
					resolver, err := oci.NewResolver(te)
					require.NoError(t, err)
					fc, err := filecache.NewFileCache(filepath.Join(root, "filecache"), 30_000_000_000, false)
					require.NoError(t, err)
					defer fc.Close()
					flags.Set(t, "executor.local_cache_store_ext4_images", !strings.HasPrefix(config, "legacy"))
					flags.Set(t, "executor.ext4_image_temp_in_filecache", config == "filecache-local-temp")
					flags.Set(t, "executor.legacy_ext4_use_manifest_digest", config == "legacy-manifest")
					require.NoError(t, exec.Command("sync").Run())
					require.NoError(t, os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0))
					start, before := time.Now(), sampleIO(t)
					var phases []phaseResult
					phase := func(name string, fn func() error) {
						begin, b := time.Now(), sampleIO(t)
						require.NoError(t, fn())
						phases = append(phases, phaseResult{name, time.Since(begin).Seconds(), sampleIO(t).sub(b)})
					}
					var img ctr.Image
					warmLayers := os.Getenv("OCICONV_IO_WARM_LAYERS") == "1"
					if warmLayers {
						require.NotEqual(t, "direct", fetchMode)
						seed, err := resolver.Resolve(ctx, image, oci.RuntimePlatform(), oci.Credentials{}, useFetcher)
						require.NoError(t, err)
						ls, err := seed.Layers()
						require.NoError(t, err)
						for _, l := range ls {
							rc, err := l.Compressed()
							require.NoError(t, err)
							_, err = io.Copy(io.Discard, rc)
							require.NoError(t, err)
							require.NoError(t, rc.Close())
						}
						require.NoError(t, exec.Command("sync").Run())
						require.NoError(t, os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0))
						start, before = time.Now(), sampleIO(t)
					}
					phase("resolve", func() error {
						var err error
						img, err = resolver.Resolve(ctx, image, oci.RuntimePlatform(), oci.Credentials{}, useFetcher)
						return err
					})
					tmp, err := createTempDiskImage(fc, root)
					require.NoError(t, err)
					require.NoError(t, tmp.Close())
					phase("fetch-extract-mke2fs", func() error {
						return ConvertContainerToExt4FS(ctx, img, root, image, tmp.Name())
					})
					phase("publish", func() error {
						_, err := publishDiskImage(ctx, img, fc, root, image, tmp.Name())
						return err
					})
					phase("temp-unlink", func() error {
						err := os.Remove(tmp.Name())
						if os.IsNotExist(err) {
							return nil
						}
						return err
					})
					output := filepath.Join(root, "linked.ext4")
					phase("cache-link", func() error { return LinkCachedImage(ctx, fc, root, image, output) })
					fi, err := os.Stat(output)
					require.NoError(t, err)
					st := fi.Sys().(*syscall.Stat_t)
					var compressed int64
					layers, err := img.Layers()
					require.NoError(t, err)
					for _, l := range layers {
						n, err := l.Size()
						require.NoError(t, err)
						compressed += n
					}
					chunks := filepath.Join(root, "chunks")
					require.NoError(t, os.Mkdir(chunks, 0755))
					var cow *copy_on_write.COWStore
					phase("cow-chunk", func() error {
						var err error
						cow, err = copy_on_write.ConvertFileToCOW(ctx, te, output, int64(os.Getpagesize()*1000), chunks, "", false, 8)
						return err
					})
					defer cow.Close()
					phase("cow-digest-cache", func() error {
						for _, c := range cow.SortedChunks() {
							d, err := c.Digest()
							if err != nil {
								return err
							}
							if err := fc.AddFile(ctx, &repb.FileNode{Digest: d}, filepath.Join(chunks, fmt.Sprint(c.Offset))); err != nil {
								return err
							}
						}
						return nil
					})
					var casAllocated int64
					if fetchMode != "direct" {
						require.NoError(t, filepath.Walk(filepath.Join(root, "cas"), func(path string, info os.FileInfo, err error) error {
							if err != nil {
								return err
							}
							if info.Mode().IsRegular() {
								casAllocated += info.Sys().(*syscall.Stat_t).Blocks * 512
							}
							return nil
						}))
					}
					var chunkAllocated int64
					chunkEntries, err := os.ReadDir(chunks)
					require.NoError(t, err)
					for _, e := range chunkEntries {
						info, err := e.Info()
						require.NoError(t, err)
						chunkAllocated += info.Sys().(*syscall.Stat_t).Blocks * 512
					}
					result := map[string]any{"cas_allocated_bytes": casAllocated, "chunk_size": os.Getpagesize() * 1000, "chunk_files": len(chunkEntries), "chunk_allocated_bytes": chunkAllocated, "image": image, "fetch_mode": fetchMode, "warm_layers": warmLayers, "config": config, "run": run, "phases": phases, "total": sampleIO(t).sub(before), "seconds": time.Since(start).Seconds(), "apparent_bytes": fi.Size(), "allocated_bytes": st.Blocks * 512, "compressed_bytes": compressed}
					b, err := json.Marshal(result)
					require.NoError(t, err)
					fmt.Printf("OCICONV_IO %s\n", b)
					if dir := os.Getenv("OCICONV_IO_KEEP"); dir != "" {
						require.NoError(t, os.MkdirAll(dir, 0755))
						// Keep only the first sample for correctness checks, without copying.
						if run == 1 {
							require.NoError(t, os.Link(output, filepath.Join(dir, fmt.Sprintf("%s-%s.ext4", strings.NewReplacer("/", "_", ":", "_").Replace(image), config))))
						}
					}
				})
			}
		}
	}
}
