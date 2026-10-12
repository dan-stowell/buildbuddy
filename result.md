# Ext4 conversion IO audit

The conversion core is unchanged. Three opt-in changes remove work around it: create the image on the filecache filesystem, skip the legacy image hash, and serialize shared-cache population across credentials. All new flags are internal and default to false.

## Changes

| Flag | Behavior | Saving / benefit |
|---|---|---|
| `executor.ext4_image_temp_in_filecache` | Create the temporary image in `FileCache.TempDir()` when storing ext4 in filecache; unpacking remains under `cacheRoot`. | No data IO saving on one filesystem: insertion was already a hardlink. Fixes cross-filesystem insertion failing with EXDEV, avoiding an entire wasted conversion on each retry. |
| `executor.legacy_ext4_use_manifest_digest` | Name legacy generations `sha256-<OCI manifest digest>` instead of hashing the generated image. | Removes one full apparent-image read and SHA256 pass. Also avoids materializing unwritten extents in the page cache before COW chunking, which reduces later chunk writes. Existing lookup, linking and migration accept this directory name. |
| `executor.serialize_ext4_conversions` | Keep credential-specific singleflight results, but lock shared-cache population by cache location and image ref before rechecking the cache. | Concurrent successful callers using different credentials do one conversion. A failed pull does not share its error with a caller using different credentials. |

The helper split in `ociconv.go` lets the benchmark measure temporary-file creation and publication using production code. `ConvertContainerToExt4FS`, tar arguments, extraction, sizing and mke2fs are untouched.

## Measurement

Measured on the exe.dev VM on 2026-10-12, Linux amd64, ext4 host filesystem, approximately 8 GB RAM, e2fsprogs 1.47.0, Go 1.27.2. The requested large RBE image is available; its compressed layers total 398,237,469 bytes and its generated image is over 1.4 GB apparent size.

The opt-in Bazel Go test `io_benchmark_test` resolves real registry images and runs production conversion and publication helpers, cache linking, production `ConvertFileToCOW`, and production chunk BLAKE3 digest computation and filecache insertion. Each sample has a fresh ext4 cache. The CAS is also fresh unless explicitly seeded for the warm-layer-cache experiment. Each configuration is run three times after `sync` and writing `3` to `/proc/sys/vm/drop_caches` as root. Warm experiments seed compressed layers, sync, then drop page cache again before timing. This means warm CAS *contents* with cold disk cache, not a warm RAM read.

At phase boundaries the test reads `/proc/self/io` and `getrusage(RUSAGE_SELF/RUSAGE_CHILDREN)`. Linux includes threads and waited-for subprocesses in the process IO counters; tar and mke2fs have both exited and been waited for before the conversion phase ends. `in_blocks/out_blocks * 512` cross-check the charged read/write byte counters. `rchar/wchar` and syscall counts are retained too. Physical `read_bytes` can be zero for substantial logical reads because production phases naturally reuse the page cache. Counters include code/certificate/database IO and small benchmark overhead; compare phase counters and repeated samples, not just noisy total reads. Mmap hashing does not increment `rchar`, although page faults can increment `read_bytes`.

`write_bytes` reports charged dirty bytes, including writes later cancelled by removal of the unpack directory. `cancelled_write_bytes` is reported separately, rather than silently treating all dirty bytes as media writes. For small images most unpack writes are cancelled; the large conversion lasts long enough for writeback to flush that tree. These are process IO accounting counters, not hardware block-operation counts. `read_syscalls/write_syscalls` include pipe IO and counter snapshots. The report also gives concrete chunk-file counts.

Fetch and extraction overlap in a stream, so the unchanged fetch/extract/mke2fs core is reported as one phase. Resolve, publication/hash, temporary cleanup, link, chunk creation and chunk digest/cache phases are separate. The test does not boot a VM or measure guest writes, snapshot manifest writes, dirty-chunk writes, remote snapshot uploads or snapshot resume; the chunk-cache-miss path is exercised directly. Chunk size is the current Firecracker value, 1000 pages (4,096,000 bytes), with eight copy workers. Chunk digest/cache work is measured sequentially; production snaploader can hash/cache chunks concurrently.

`direct` disables OCI blob caching. `direct-cache` sets the resolver's cache percentage to 100 and provides an authenticated context. `fetcher` enables `executor.use_oci_fetcher` and uses the service. For cache experiments the actual disk-backed CAS and gRPC service run in the benchmark process, so counters include both executor and server IO. Production remote CAS IO occurs on the cache server, not necessarily the executor; do not attribute those compressed-layer writes to executor-local storage. No external BuildBuddy cache deployment is required.

Apparent and allocated image sizes are `stat.Size` and `stat.Blocks*512`, independently checked using `du -B1` and `du -B1 --apparent-size` during mounted-tree validation. Times and counters in tables are means of three samples; bytes are rounded to integers. Raw phase samples are in `enterprise/server/oci/ociconv/benchmark/results.jsonl`; the full phase table is in `enterprise/server/oci/ociconv/benchmark/phases.md`.

## Results

| Image | Fetch | Config | n | Written bytes | Read bytes | Cancelled bytes | Seconds | Apparent bytes | Allocated bytes |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| mirror.gcr.io/library/alpine:3.20 | direct | filecache-baseline | 3 | 25722880 | 15209813 | 8073216 | 1.509 | 16904192 | 12972032 |
| mirror.gcr.io/library/alpine:3.20 | direct | filecache-local-temp | 3 | 25725611 | 3074731 | 8073216 | 2.796 | 16904192 | 12972032 |
| mirror.gcr.io/library/alpine:3.20 | direct | legacy-hash | 3 | 29704192 | 3074731 | 8073216 | 1.846 | 16904192 | 12972032 |
| mirror.gcr.io/library/alpine:3.20 | direct | legacy-manifest | 3 | 25722880 | 3074731 | 8073216 | 1.658 | 16904192 | 12972032 |
| mirror.gcr.io/library/ubuntu:24.04 | direct | filecache-baseline | 3 | 261861376 | 20393984 | 84897792 | 2.350 | 131080192 | 92745728 |
| mirror.gcr.io/library/ubuntu:24.04 | direct | filecache-local-temp | 3 | 261861376 | 3074731 | 84897792 | 2.318 | 131080192 | 92745728 |
| mirror.gcr.io/library/ubuntu:24.04 | direct | legacy-hash | 3 | 266126677 | 3100672 | 84897792 | 3.004 | 131080192 | 92745728 |
| mirror.gcr.io/library/ubuntu:24.04 | direct | legacy-manifest | 3 | 261861376 | 3076096 | 84897792 | 2.320 | 131080192 | 92745728 |
| gcr.io/flame-public/rbe-ubuntu24-04:latest | direct | filecache-baseline | 3 | 3290660864 | 3115691 | 0 | 13.987 | 1410621440 | 1134006272 |
| gcr.io/flame-public/rbe-ubuntu24-04:latest | direct | filecache-local-temp | 3 | 3290660864 | 11643563 | 0 | 13.891 | 1410621440 | 1134006272 |
| gcr.io/flame-public/rbe-ubuntu24-04:latest | direct | legacy-hash | 3 | 3324304043 | 11628544 | 0 | 15.027 | 1410621440 | 1134006272 |
| gcr.io/flame-public/rbe-ubuntu24-04:latest | direct | legacy-manifest | 3 | 3290660864 | 11609429 | 0 | 13.937 | 1410621440 | 1134006272 |
| mirror.gcr.io/library/alpine:3.20 | direct-cache | filecache-baseline | 3 | 29384704 | 22594901 | 8073216 | 2.079 | 16904192 | 12972032 |
| mirror.gcr.io/library/ubuntu:24.04 | direct-cache | filecache-baseline | 3 | 291651584 | 18923520 | 84897792 | 2.959 | 131080192 | 92745728 |
| gcr.io/flame-public/rbe-ubuntu24-04:latest | direct-cache | filecache-baseline | 3 | 3689014613 | 10984107 | 0 | 15.250 | 1410621440 | 1134006272 |
| mirror.gcr.io/library/alpine:3.20 | fetcher | filecache-baseline | 3 | 29387435 | 22233088 | 8073216 | 2.250 | 16904192 | 12972032 |
| mirror.gcr.io/library/ubuntu:24.04 | fetcher | filecache-baseline | 3 | 291652949 | 3100672 | 84897792 | 2.995 | 131080192 | 92745728 |
| gcr.io/flame-public/rbe-ubuntu24-04:latest | fetcher | filecache-baseline | 3 | 3689024171 | 18818389 | 0 | 16.211 | 1410621440 | 1134006272 |
| mirror.gcr.io/library/alpine:3.20 | direct-cache-warm | filecache-baseline | 3 | 25725611 | 15078741 | 8073216 | 0.966 | 16904192 | 12972032 |
| mirror.gcr.io/library/ubuntu:24.04 | direct-cache-warm | filecache-baseline | 3 | 261865472 | 33172139 | 84897792 | 1.806 | 131080192 | 92745728 |
| gcr.io/flame-public/rbe-ubuntu24-04:latest | direct-cache-warm | filecache-baseline | 3 | 3290674517 | 401622357 | 0 | 10.773 | 1410621440 | 1134006272 |
| mirror.gcr.io/library/alpine:3.20 | fetcher-warm | filecache-baseline | 3 | 25726976 | 22855680 | 8073216 | 0.579 | 16904192 | 12972032 |
| mirror.gcr.io/library/ubuntu:24.04 | fetcher-warm | filecache-baseline | 3 | 261865472 | 40834389 | 84897792 | 1.398 | 131080192 | 92745728 |
| gcr.io/flame-public/rbe-ubuntu24-04:latest | fetcher-warm | filecache-baseline | 3 | 3290679979 | 401651029 | 0 | 10.818 | 1410621440 | 1134006272 |

The total includes conversion, publication/linking and the cold chunk-consumption path. In nonchunk mode omit the chunk/digest phases; in a chunk-cache hit the conversion itself is skipped. Negative result: filecache temp placement has no data-byte benefit on this one-filesystem host. Alpine's temp-placement wall-time regression was mostly in manifest resolution (network timing), while insertion/linking remained zero data IO. Total read variation also includes executable/library faults and is not evidence that temp placement eliminates those reads.

### Attributable legacy and chunk savings

| Image | Apparent bytes no longer SHA256-read | Legacy publication time (hash → manifest), seconds | Chunk writes (hash → no hash), bytes | Chunk files (hash → no hash) | Avoided chunk bytes |
|---|---:|---:|---:|---:|---:|
| mirror.gcr.io/library/alpine:3.20 | 16,904,192 | 0.01345 → 0.00018 | 12,967,936 → 8,990,720 | 5 → 4 | 3,977,216 |
| mirror.gcr.io/library/ubuntu:24.04 | 131,080,192 | 0.10565 → 0.00024 | 92,741,632 → 88,477,696 | 26 → 24 | 4,263,936 |
| gcr.io/flame-public/rbe-ubuntu24-04:latest | 1,410,621,440 | 1.03131 → 0.00026 | 1,134,002,176 → 1,100,361,728 | 281 → 273 | 33,640,448 |

Publication's physical read counter was **zero** in every legacy-hash sample: the image was hot. Thus the directly attributable benefit is the removed apparent-byte read, SHA256 CPU work and subsequent zero-extent copies, not a claimed A-byte physical read reduction in this test. Removing the hash saves approximately 517, 4,002 and 43,050 read calls including EOF for these images (counter snapshots excluded).

### Cold local IO budget and fleet blob storage

| Image | Compressed layers C | Core charged writes (no OCI cache) | COW copied extents D | Non-hole chunk files N | Cold CAS allocated storage (fetcher, mean) |
|---|---:|---:|---:|---:|---:|
| mirror.gcr.io/library/alpine:3.20 | 3,630,321 | 16,728,064 | 8,990,720 | 4 | 3,657,728 |
| mirror.gcr.io/library/ubuntu:24.04 | 29,764,422 | 173,379,584 | 88,477,696 | 24 | 29,786,112 |
| gcr.io/flame-public/rbe-ubuntu24-04:latest | 398,237,469 | 2,190,295,040 | 1,100,361,728 | 273 | 398,333,269 |

For the large image, one avoided duplicate conversion saves about **2.19 GB of charged core writes** before chunking, along with a second extraction, mke2fs tree read and registry transfer. Avoiding a redundant cold chunk conversion saves about **1.10 GB of copied writes** and 273 new chunk files; representing the base as monolithic ranges could remove that pass even for the first consumer. The full phase table records the associated read/write syscall counts and wall times.

Populated-CAS runs remove about C server writes and replace upstream network transfer with about C server reads. For RBE these runs read approximately 401.6 MB total (398.2 MB compressed layers plus small process/code/metadata reads) and take about 10.8 seconds, versus 15–16 seconds for the cached-service cold miss here. This supports retaining shared caching where there is reuse.

Initial exploration used nominal 4 MiB chunks; it showed the same extent-materialization effect. Those exploratory samples are excluded from the final table, which uses the exact production 1000-page chunk size. No buffered filecache copy or decompressed layer spool was found to remove.

## Correctness and tests

All twelve retained real-image outputs (baseline and three variants for each image) passed `e2fsck -fn` and mounted with `mount -o loop,ro`. The tree checker compares every path's SHA256 contents, mode/type, uid/gid, symlink target, hardlink equivalence classes, device major/minor, xattrs and mtime. It does not follow guest symlinks into the host.

Strict independent-conversion tree comparisons **do not pass exact directory-mtime equality**, even for the unchanged baseline. Alpine and Ubuntu have two differences: the generated root directory and empty mke2fs `lost+found` directory mtimes. RBE has those two plus 85 directory mtimes set during extraction. A separately repeated RBE **baseline-versus-baseline** conversion produced the exact same 85 additional differing paths, all differing only in directory mtime. No regular-file content or mtime differences, mode/owner differences, symlink differences, hardlink differences, device differences or xattr differences were found. This is an existing nondeterminism in the core, not silently counted as a strict pass. Fixing extraction timestamp handling belongs to the sibling core work and is not attempted here.

With only these confirmed generated directory mtimes explicitly excepted, all variants match their baseline trees. The checker defaults to strict comparison; `--allow-generated-times` excludes only root and empty `lost+found`, and the separate explicit RBE directory list was derived from baseline-versus-baseline comparison. It never ignores all mtimes or normal file mtimes.

| Image | Tree entries | Regular files | Symlinks | Hardlink groups | Strict differing paths per variant | Differences after confirmed generated-time exceptions |
|---|---:|---:|---:|---:|---:|---:|
| Alpine | 519 | 87 | 334 | 0 | 2 | 0 |
| Ubuntu | 3,445 | 2,587 | 197 | 2 | 2 | 0 |
| RBE | 16,760 | 13,263 | 1,296 | 6 | 87 | 0 |

These real-image outputs contain no device nodes or nonempty xattrs, so a separate **frozen ext4 metadata fixture** supplies positive coverage. It includes a character device (1:3), a `user.ociconv` xattr, a regular file and hardlink with mode 0751, uid 1234/gid 2345, a fixed 2009 mtime, and a relative symlink. Baseline and all four publication modes mount and pass e2fsck; their complete ext4 image SHA256 values are identical, so all inode metadata and generated filesystem timestamps are preserved exactly through publication/linking. This validates the changed work without conflating it with a second run of the nondeterministic core. The mounted fixture's individual metadata/content assertions also pass.

Validation:

- `bazel test //enterprise/server/oci/ociconv:ociconv_test //enterprise/server/oci/ociconv:io_benchmark_test //enterprise/server/remote_execution/filecache:all`: all three test targets pass. The two root/network integration checks skip by default in the benchmark target and were run explicitly outside the sandbox. The regular ociconv suite covers both temp-location settings, a real cacheRoot-on-tmpfs/filecache-on-ext4 case, manifest-named legacy lookup/link/migration, credential-specific failure recovery with serialization off/on, and one layer GET for simultaneous callers with different credentials.
- All 72 final benchmark samples pass. A second baseline conversion used only for correctness confirmation also passes. `rusage` read/write block totals match the charged byte counters at every phase boundary in the final samples.
- Root-only `TestPublicationMetadata`: baseline plus all four modes pass exact byte equality, loop mounts, metadata assertions and e2fsck.
- The additionally attempted, untouched `//enterprise/server/util/ext4:ext4_test` has one pre-existing failure: `TestDirectoryToImageAutoSize_Reproducible` gives different hashes after the test changes atime/ctime. Installed mke2fs is 1.47.0; the existing reproducibility flag explicitly requires 1.47.2+. Other ext4 tests pass. Reproducibility mode was not used in these measurements or changed to hide this failure.

## Tests added

Concurrent authentication tests hold the first registry request open until the second caller is waiting. With invalid credentials first, that caller receives a 401 and the valid caller independently converts and links the image. With valid credentials first, conversion succeeds and the invalid caller reuses the populated cache, preserving the existing cached-image access behavior.

The cancellation test blocks the holder's layer fetch and starts two callers with different credentials. A test-only lock implementation signals only after a worker fails `TryLock` on the held per-key mutex. The test waits for both contention signals before cancelling one caller; no sleeps establish ordering. It returns `context.Canceled` within one second, before the holder is released. The holder and remaining waiter then succeed, share an inode, and fetch the layer only once.

The concurrent successful-caller test now runs in both filecache and legacy-cache modes (`executor.local_cache_store_ext4_images=false`). A blocked layer fetch keeps the holder in flight while the other credential pair waits; both outputs share an inode and only one layer GET occurs. These added tests require no root privileges. The same contention hook now orders the authentication and successful serialization tests as well. Production still uses the existing lockmap; only its acquisition function is replaceable by tests, with no conversion behavior changes.

The opt-in IO benchmark and mounted metadata fixture now skip cleanly when enabled without root, before performing privileged setup.

Validation for this follow-up (both targets pass uncached):

- Non-root: `bazel test --nocache_test_results --test_env=OCICONV_IO_BENCH=1 --test_env=OCICONV_METADATA_CHECK=1 //enterprise/server/oci/ociconv:ociconv_test //enterprise/server/oci/ociconv:io_benchmark_test`. All conversion/concurrency tests run; both enabled privileged tests report their root-required skip reasons.
- Root: `bazel test --nocache_test_results --test_strategy=standalone --spawn_strategy=local --test_arg=-test.v --run_under='sudo -n env OCICONV_IO_BENCH=1 OCICONV_METADATA_CHECK=1 OCICONV_IO_IMAGES=mirror.gcr.io/library/alpine:3.20 OCICONV_IO_RUNS=1' //enterprise/server/oci/ociconv:ociconv_test //enterprise/server/oci/ociconv:io_benchmark_test`. All concurrency cases run. The benchmark executes one Alpine sample for each of the four publication modes, and the mounted metadata fixture executes baseline plus all four modes, including byte equality, e2fsck, device, xattr, ownership, hardlink and timestamp checks.

## Reproduce

Run from the repository root on a dedicated machine. The opt-in benchmark drops the host page cache. Build once, then run the binary as root outside the Bazel sandbox:

```sh
bazel build //enterprise/server/oci/ociconv:io_benchmark_test
sudo env OCICONV_IO_BENCH=1 OCICONV_IO_KEEP=/tmp/ociconv-correctness \
  bazel-bin/enterprise/server/oci/ociconv/io_benchmark_test_/io_benchmark_test \
  -test.run=TestConversionIO -test.v -test.timeout=60m > direct.log
```

Defaults are the three requested images, three runs and all four main configurations. A keep directory must be fresh: only the first sample of each config is hardlinked there. To measure both cache fetch paths, run each of the following modes separately with a fresh process, retaining logs:

```sh
sudo env OCICONV_IO_BENCH=1 OCICONV_IO_CONFIGS=filecache-baseline \
  OCICONV_IO_FETCH_MODE=direct-cache \
  bazel-bin/enterprise/server/oci/ociconv/io_benchmark_test_/io_benchmark_test \
  -test.run=TestConversionIO -test.v -test.timeout=60m > direct-cache.log
```

Repeat that command with `OCICONV_IO_FETCH_MODE=fetcher`. For populated-CAS/cold-page-cache runs add `OCICONV_IO_WARM_LAYERS=1` to each mode. Optional `OCICONV_IO_IMAGES` and `OCICONV_IO_CONFIGS` are comma-separated; `OCICONV_IO_RUNS` overrides the repeat count. Raw samples or logs can both be summarized:

```sh
python3 enterprise/server/oci/ociconv/benchmark/summarize.py direct.log direct-cache.log
python3 enterprise/server/oci/ociconv/benchmark/summarize.py \
  enterprise/server/oci/ociconv/benchmark/results.jsonl
sudo env OCICONV_METADATA_CHECK=1 \
  bazel-bin/enterprise/server/oci/ociconv/io_benchmark_test_/io_benchmark_test \
  -test.run=TestPublicationMetadata -test.v
sudo python3 enterprise/server/oci/ociconv/benchmark/check_trees.py \
  /tmp/ociconv-correctness/mirror.gcr.io_library_alpine_3.20-filecache-baseline.ext4 \
  /tmp/ociconv-correctness/mirror.gcr.io_library_alpine_3.20-legacy-manifest.ext4
```

The last command deliberately reports strict generated-mtime differences. For Alpine/Ubuntu the explicit exception is `--allow-generated-times`. For the measured RBE manifest (`sha256:5d2d8b3935c4dad4d81e0b6d89eeb06734d0e708cdf3d8ec779cf619c9243860`, linux/amd64), also pass `--generated-mtime-paths enterprise/server/oci/ociconv/benchmark/rbe_generated_directory_mtimes.txt`. Reconfirm that list with baseline-versus-baseline when testing a different image version.

## Source trace and IO budget

Let **C** be total compressed layer bytes, **R** the flattened rootfs data, **A** apparent ext4 image size, **E** allocated image bytes, **D** the extents copied into COW chunks, and **N** the number of non-hole chunks. Sizes differ: unwritten extents are allocated but `SEEK_DATA` can still treat them as holes.

### Fetch

`enterprise/server/util/oci/oci.go`: `Resolver.Resolve` constructs manifest-backed layers. `Layers` does not need the image config or layer DiffIDs. `mutate.Extract` visits layers in reverse order once and asks for `Uncompressed`; it does not invoke `DiffID` and perform another layer pass. The vendored remote layer's `Compressed` returns the HTTP blob reader. Our layer `Uncompressed` wraps compressed bytes in a streaming decompressor.

Without OCI fetcher, cache hits stream through `ocicache.FetchBlobFromCache` into an `io.Pipe`. Cache misses stream HTTP bytes; optional `NewBlobReadThroughCacher` uploads compressed OCI bytes as they are consumed. There is **no local compressed-layer temporary file, full decompressed tar spool, or write/read-back before extraction**. Only the extracted final rootfs is written locally. With OCI fetcher, `FetchBlob` streams gRPC messages directly into `streamReader`; the server streams registry bytes to both response and CAS, or streams a CAS hit. It does not spool the blob either. The server checks cached metadata and access proof; credential-specific blob singleflight leaders stream while followers subsequently read CAS. The resolver does not populate CAS a second time when using fetcher.

Caching a cold blob therefore adds roughly **C writes on the CAS server**, plus small metadata writes, and zero executor-local layer writes. A cold-page-cache CAS hit adds roughly **C server reads**, with no new compressed-data writes. This is compressed OCI data (usually gzip) in CAS even though the CAS RPC itself can use ZSTD transport compression. Ext4 and compressed OCI bytes coexist in the fleet when both caches are enabled; they are not duplicate executor-local files by default. This reuse is valuable for other executors, OCI runtimes, and images sharing layers. Disabling the OCI cache would save C server writes on a one-off miss but sacrifice that reuse, so it is not proposed as a universal improvement.

### Filecache

`enterprise/server/remote_execution/filecache/filecache.go`: `AddFile` stats and accounts allocated disk usage, ensures the parent exists, takes the cache lock, removes any tracked old entry, calls `cloneOrLink`, and tracks the new entry. It **does not rename or byte-copy** the image. Both insertion and `FastLinkFile` use the same helper. `WithSharedDirectory` selects `_SHARED_ext4_images`, not the anonymous namespace; normally this chooses `fastcopy.FastCopy`.

`server/util/fastcopy/fastcopy_linux.go`: `FastCopy` is `os.Link`. `Clone` is also a hardlink by default; when `executor.enable_fastcopy_reflinking` is true it uses `FICLONE`. `executor.local_cache_always_clone` or the anonymous namespace selects `Clone`, but neither Linux implementation falls back to a buffered copy. Unsupported reflink or cross-filesystem links return an error. Thus AddFile + cleanup + use ordinarily add **0 image-data reads/writes**, with one insertion link, one temp unlink, and one use link, plus stat/mkdir/LRU metadata operations. Hardlinked temp and cached image do not occupy two sets of data blocks.

Creating the image under the existing filecache temp directory puts it on the same filesystem as normal cache entries. No new rename API is necessary and using rename in AddFile would violate its contract for callers retaining their source. The new flag fixes population across cacheRoot/filecache filesystems. Legacy-to-filecache migration also still links its existing source and cannot migrate across filesystems without a separate transfer. The final output/chroot must still share the filecache filesystem (or support the requested clone); moving the temporary file does not solve an output path on another filesystem. Nested mounts within the filecache layout are also outside the same-filesystem assumption.

### Legacy hash

The legacy generation directory name was a hash of the *generated ext4 bytes*, including nondeterministic filesystem metadata. Readers only sort generation directories by mtime and look for `containerfs.ext4`; they do not verify this hash. The OCI manifest digest already identifies the source generation, so recomputing a digest of A bytes is unnecessary for this layout. The flag removes **A logical bytes read, about ceil(A/32768) read calls, and a full SHA256 CPU pass**. Hot-cache physical reads are often zero; it would save up to the allocated readable extents on a genuinely cold image. Migration accepts both generation name formats.

The extra effect observed here is Linux unwritten-extent handling: reading the image's allocated zero ranges during `hashFile` changes what a subsequent `SEEK_DATA` sees in the page cache, causing COW conversion to copy more zero data. Skipping hashing avoids those later data writes and extra chunk files. This is filesystem/page-cache dependent, not a promised universal byte saving.

### VM startup and snapshots

`enterprise/server/remote_execution/containers/firecracker/firecracker.go`: cold startup links containerfs into the chroot. With chunked snapshot sharing disabled, it is a read-only container drive and is not copied into a writable root image; a separate writable scratch image is used. With sharing enabled, `initRootfsStore` first reuses a cached containerfs snapshot. `snaploader.UnpackContainerImage` also checks the chunk cache before converting, so a warm chunk snapshot already avoids pulling/converting a monolithic image and avoids chunking it again.

On a chunk miss, `copy_on_write.ConvertFileToCOW` opens the image per worker, uses `SEEK_DATA/SEEK_HOLE`, creates/truncates offset-named files, and copies extents using Go's `io.Copy`/`copy_file_range` fast path. On this ext4 host that creates another **D data writes and D logical source reads**, plus N chunk create/truncate/close operations, extent seeks and range-copy calls. It skips fully empty chunks and preserves holes. It does not blindly copy A bytes. The following snapshot-cache pass computes BLAKE3 chunk digests via mmap (another logical pass through nonempty chunks, including zeros within those chunks), then adds chunks to filecache with links/clones. No additional full chunk-data copy is imposed by AddFile. Dirty chunks and optional remote upload can add IO later; neither is required to cache this initial immutable base locally.

The monolith and chunk files can coexist on disk: about E + D local data-block storage on an ext4 host. The chroot's monolith link, cached monolith and temp monolith are one inode, not three copies; similarly the chunk directory and linked chunk cache share their blocks. Existing chunk-snapshot cache hits avoid this conversion entirely. There is no startup copy to remove on that hit path. Nonsharing snapshot saves use a synthetic key plus stat size for files, explicitly avoiding a full digest pass; local snapshot cache insertion is again a link. The compatibility path for a sharing snapshot without a rootStore can still include the monolithic containerfs and compute its BLAKE3 digest. That adds A logical reads, and optional remote upload adds another pass; normal new rootStore snapshots contain chunks instead.

### Concurrency and eviction

Original conversion singleflight includes cache location, image ref and credentials. Two different credential pairs can both check `IsImageCached == false` and convert the same ref concurrently. `container.PullImageIfNecessary` serializes by isolation type and ref only when both authenticators are configured. Its early fallback with either authenticator missing calls `PullImage` directly, and direct conversion callers also bypass that outer lock. Thus the old comment claiming the outer lock always prevents duplication was too strong.

The new optional lock spans the cache recheck and population, keyed by the shared filecache location/ref (or legacy cacheRoot/ref). Each credential-specific singleflight retains its own failure result. After successful population, the next caller rechecks and uses the image. After a failure, the next caller pulls with its own credentials. Existing caller authorization responsibilities remain unchanged: cached-image access is not reauthenticated by `CreateDiskImage`. Both failure recovery and one layer GET across two credential pairs are tested.

Each avoided successful duplicate saves another entire fetch/extract/mke2fs operation: approximately R + image-data writes, R logical mke2fs reads, all extraction/inode operations, and A hashing bytes in legacy hash mode; optional CAS can also avoid another C upload. The shared lock does not solve different refs pointing at one manifest, cross-process conversion races, or cache eviction between publication and the final link. Concurrent cold chunk-snapshot misses in `UnpackContainerImage` also have no equivalent singleflight around chunk population and can each incur D copied bytes and N new files. Those are follow-up opportunities.

## Larger ideas (not implemented)

| Idea | Estimated saving for one cold consumption | Reason to defer |
|---|---|---|
| Represent immutable COW chunks as ranges of a pinned monolithic image | Avoid D chunk writes and D copy reads; avoid N chunk files; reduce base storage from E+D to E. Chunk hashing remains. | COW mmap/read/dirty-copy paths currently assume one file per chunk. Need offset-aware mapping, lifetime/pinning across cache eviction, snapshot compatibility and correct first-write isolation. |
| Range-reflink extents into chunk files on XFS/Btrfs | Avoid most D physical chunk writes and duplicate blocks; retain N metadata objects and digest pass. | ext4 here does not support FICLONERANGE. copy_file_range may already clone on supporting filesystems; alignment, sparse extents, cross-filesystem fallback and tail handling require coverage. |
| Produce chunks directly while building the ext4 image | Avoid the monolith-to-chunks D read + D write pass and possibly monolith retention. | mke2fs expects seekable output. Requires a chunk-aware block/file adapter or integration with the sibling core-conversion work; outside this task. |
| Coalesce first chunk-snapshot population, then unpack the cached manifest for waiters | Save (k-1)*D copy writes/reads and (k-1)*N files for k concurrent VM misses. | Must distinguish instance/access/remote-write settings, retain per-VM store lifetimes and handle failed/evicted chunk manifests correctly. |
| Key converted images by authenticated source digest/platform across ref aliases | Save a complete conversion for each duplicate tag/ref, with only reference metadata/link overhead. | Requires authentication before coalescing, tag refresh rules, ref-to-content index/migration and eviction accounting. |
| Hash chunks while copying them | Remove a second logical chunk-data read, up to D physical reads on a cold cache. | Hot-cache hashing measured no physical reads here; retaining copy_file_range avoids user-space copies. A fused buffered path may lose that advantage, and hashes include sparse zeros within chunks. Benchmark before adopting. |
| Discard monolithic images after durable chunk publication on chunk-only executors | Reclaim E allocated bytes of retained storage. | Does not remove initial copy IO; requires proving nonchunk callers/fallback paths cannot need the monolith and coordinating eviction/access lifetimes. |

## Risks and compatibility

All new flags default off. Temp placement shifts where image allocation and ENOSPC occur; its source needs a functioning filecache temp directory, and does not grant cross-filesystem output linking. Always-clone/reflink configurations continue to use their existing semantics. Legacy manifest naming is not a checksum of ext4 bytes and must not be advertised as one; source bytes can produce different filesystem metadata on separate conversions. Directory lookup and migration remain backward compatible and replacement uses rename, so already linked images retain their old inode. Mutable-tag freshness behavior is unchanged.

The credential-independent lock is process-local, uses the existing lockmap GC, and only protects publication. Waiting singleflight callers can cancel promptly, but a background operation waiting on the non-context-aware lock returns only after acquiring it; it checks cancellation before doing any work. Conversion timeout remains 15 minutes. Cache eviction can still cause linking to fail after conversion. Removing credentials from singleflight outright was rejected because an unauthorized leader's failure could be shared with an authorized waiter.

Changing tree population, ext4 features, file contents, permissions, device handling, xattr handling or timestamp policy was deliberately out of scope. The current conversion has its own metadata limitations (for example, tar's current xattr options); this work preserves the baseline rather than claiming to repair those limitations.

## Recommendation

Stage `executor.ext4_image_temp_in_filecache` for deployments with separate cacheRoot/filecache filesystems, and `executor.serialize_ext4_conversions` where callers can bypass the outer pull lock. Enable `executor.legacy_ext4_use_manifest_digest` for remaining legacy-cache deployments: it removes a redundant full-image SHA256 pass and, on this host, also reduces chunk writes. On normal one-filesystem filecache deployments, temp placement is a correctness improvement, not a throughput optimization. Keep OCI compressed-layer caching where shared reuse justifies it. The largest remaining local IO opportunity outside the extraction/build core is an offset-aware immutable COW base that avoids copying the monolith into chunk files.
