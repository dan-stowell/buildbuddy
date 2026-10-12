# ext4 conversion IO investigation

## Recommendation and changes

Keep the unpack-directory pipeline and keep the journal. Two internal, opt-in flags were added:

- `executor.prezeroed_ext4_images=false`: use `-E assume_storage_prezeroed=1,nodiscard` for directory images and `MakeEmptyImage`. An absent or empty **regular file** already reads as zeros. This avoids allocating zero extents without weakening fsync behavior, changing input metadata, or moving inode-table initialization into the guest. Existing nonempty outputs are still rejected; nonregular outputs are now also rejected.
- `executor.oci_unpack_tmpfs_bytes=0`: a positive byte limit enables a private, size-limited tmpfs for OCI unpacking. Compressed layer sizes provide a cheap eligibility filter; the mount enforces the expanded-size limit. Mount failure uses disk; extraction failure unmounts and retries the same tar pipeline on disk. Cancellation does not trigger a retry. All success/error paths remove the unpack directory and unmount the tmpfs.

The large image's **net conversion writes fell from 2,088.823 MiB to 1,049.419 MiB (49.8%)** with tmpfs plus prezeroed initialization; mean conversion time fell from **4.267 s to 2.798 s (34.4%)**. For Alpine/Ubuntu, unpack writes were already cancelled before writeback, so tmpfs did **not** materially reduce net writes. Prezeroed initialization alone mainly saves allocated space: approximately **4 MiB, 4 MiB, and 32 MiB** for Alpine, Ubuntu, and the large image respectively. It does not halve conversion IO.

Use the prezeroed flag for a controlled rollout on supported e2fsprogs versions. Enable tmpfs only with an explicit memory/concurrency budget and a threshold appropriate to the workload. A 256 MiB threshold covers the two small images here; the large image requires a higher limit (the measurements used 2 GiB). Leave sizing, inode policy, tar ownership/permission handling, journaling, and fsync unchanged. Existing cached images are not regenerated when flags change; improvements apply to new conversions.

## Environment and method

2026-10-12, exe.dev VM, Ubuntu 24.04, Linux 6.12.93, 2 CPUs, 7.75 GiB RAM, ext4 root filesystem on `/dev/vda`, no swap. Timed runs used the VM's original **e2fsprogs 1.47.0**, 4 KiB image blocks, and `/etc/mke2fs.conf` defaults. `dirty_background_ratio=10`, `dirty_ratio=20`, `dirty_expire_centisecs=3000`. No builds or other benchmark runs ran concurrently with the final timed matrix.

All images were fetched for linux/amd64 with `remote.Image`, then flattened with the same **`mutate.Extract`** used by production. Fetching/network transfer and writing this reusable tar were excluded from conversion timing:

| Image | Resolved platform manifest | Flattened tar | Tree entries |
| --- | --- | ---: | ---: |
| `mirror.gcr.io/library/alpine:3.20` | `sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e` | ~7.8 MiB | 518 |
| `mirror.gcr.io/library/ubuntu:24.04` | `sha256:f610ab94648195aa356059f5b41d6085c9d4d903c072430cdd1af7bdb646106b` | ~77 MiB | 3,444 |
| `gcr.io/flame-public/rbe-ubuntu24-04:latest` | `sha256:5d2d8b3935c4dad4d81e0b6d89eeb06734d0e708cdf3d8ec779cf619c9243860` | ~1,020 MiB (>1 GB) | 16,759 |

The [Go benchmark](enterprise/tools/ext4_bench/main.go) reports `/proc/self/io` before/after each phase. Linux includes children **after wait**, so tar/mke2fs are included without sampling races. It reports `write_bytes`, `read_bytes`, `cancelled_write_bytes`, and `write_bytes - cancelled_write_bytes` separately. Each configuration ran **three times**, with **`sync; echo 3 > /proc/sys/vm/drop_caches` before every repetition**, using root privileges. Tables report arithmetic means; raw individual runs are committed in [measurements](enterprise/tools/ext4_bench/measurements). Measurements are process IO counters, not device-level totals: kernel filesystem journal/metadata traffic and unrelated processes are excluded. Read counters include a local flattened tar rather than production's network stream, and include executable/library startup reads. Page-cache hits are not storage reads.

`Conversion` includes tar extraction, the production size estimator, mke2fs, a hardlink matching the default Linux filecache path, unpack deletion, and a final `sync` to expose deferred writeback. `Total` also includes sparse chunk creation and its flush. Chunk creation mirrors `ConvertFileToCOW`'s SEEK_DATA/SEEK_HOLE loop, using 4 MiB chunks. It omits service setup, manifest writes, hashing, and remote transfer; this is not a full snaploader benchmark. Production integration tests exercise conversion plus actual filecache insertion/linking separately.

Apparent bytes use `stat.Size()` (equivalent to `du --apparent-size -B1`); allocated bytes use `stat.Blocks*512` (equivalent to `du -B1`). Hardlink allocation was identical to source allocation in **every** timed run. Directory fixture mtimes were restored from tar headers after extraction, using a fixed value for implicit directories, because tar can revisit and modify previously restored directories. Benchmark-only `E2FSPROGS_FAKE_TIME` fixes the synthetic mkfs root/lost+found timestamps. File/symlink mtimes are preserved; production timestamps are unchanged. Initial pilots with timestamp noise and concurrent builds are excluded from all reported results.

The [benchmark README](enterprise/tools/ext4_bench/README.md) gives commands and configuration definitions. `compact` uses **estimate + 4 MiB**, retaining lazy initialization, journal, 5% reserved-block setting, and other baseline features. `inodes` additionally uses `-N entries + max(entries/10,128)`. `small-journal` uses `-J size=4`; `no-fsync` uses a benchmark-only LD_PRELOAD interposer and still flushes before reporting conversion totals.

## Conversion results

All sizes/IO values below are **MiB** (1,048,576 bytes), wall times are seconds, and n=3 per row. `tar-no-same` is an intentionally incompatible negative experiment; all other layouts passed correctness checks. Gross writes are not a claim that all those bytes reached storage: subtract the cancelled column.

### alpine

| Layout | Written MiB | Cancelled MiB | Net written MiB | Read MiB | Seconds | Apparent MiB | Allocated MiB |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| baseline | 15.953 | 7.699 | 8.254 | 12.658 | 0.080 | 16.121 | 12.371 |
| lazy | 15.953 | 7.699 | 8.254 | 10.760 | 0.076 | 16.121 | 8.375 |
| nodiscard | 15.953 | 7.699 | 8.254 | 12.547 | 0.072 | 16.121 | 12.371 |
| lazy-nodiscard | 15.953 | 7.699 | 8.254 | 10.762 | 0.072 | 16.121 | 8.375 |
| small-journal | 15.953 | 7.699 | 8.254 | 10.762 | 0.069 | 16.121 | 8.375 |
| no-journal | 15.949 | 7.699 | 8.250 | 10.762 | 0.069 | 16.121 | 8.371 |
| compact | 15.949 | 7.699 | 8.250 | 10.762 | 0.075 | 14.102 | 8.352 |
| inodes | 15.949 | 7.699 | 8.250 | 10.762 | 0.070 | 14.102 | 8.352 |
| tmpfs | 8.254 | 0.000 | 8.254 | 10.673 | 0.055 | 16.121 | 12.371 |
| prezeroed | 15.953 | 7.699 | 8.254 | 10.793 | 0.069 | 16.121 | 8.375 |
| tmpfs-prezeroed | 8.254 | 0.000 | 8.254 | 10.762 | 0.052 | 16.121 | 8.375 |
| no-fsync | 15.949 | 7.699 | 8.250 | 10.919 | 0.081 | 16.121 | 12.371 |
| tar-no-same | 15.953 | 7.699 | 8.254 | 12.730 | 0.077 | 16.121 | 12.371 |

### ubuntu

| Layout | Written MiB | Cancelled MiB | Net written MiB | Read MiB | Seconds | Apparent MiB | Allocated MiB |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| baseline | 165.348 | 80.965 | 84.383 | 82.720 | 0.410 | 125.008 | 88.449 |
| lazy | 165.348 | 80.965 | 84.383 | 80.000 | 0.433 | 125.008 | 84.453 |
| nodiscard | 165.348 | 80.965 | 84.383 | 80.000 | 0.403 | 125.008 | 88.449 |
| lazy-nodiscard | 165.348 | 80.965 | 84.383 | 80.000 | 0.379 | 125.008 | 84.453 |
| small-journal | 165.348 | 80.965 | 84.383 | 80.000 | 0.411 | 125.008 | 84.453 |
| no-journal | 165.344 | 80.965 | 84.379 | 80.000 | 0.435 | 125.008 | 84.449 |
| compact | 165.344 | 80.965 | 84.379 | 80.000 | 0.410 | 104.840 | 84.469 |
| inodes | 165.344 | 80.965 | 84.379 | 80.000 | 0.417 | 104.840 | 84.469 |
| tmpfs | 84.383 | 0.000 | 84.383 | 79.823 | 0.304 | 125.008 | 88.449 |
| prezeroed | 165.348 | 80.965 | 84.383 | 80.905 | 0.415 | 125.008 | 84.453 |
| tmpfs-prezeroed | 84.383 | 0.000 | 84.383 | 79.823 | 0.304 | 125.008 | 84.453 |
| no-fsync | 165.344 | 80.965 | 84.379 | 80.921 | 0.403 | 125.008 | 88.449 |
| tar-no-same | 165.348 | 80.965 | 84.383 | 84.587 | 0.434 | 125.008 | 88.449 |

### rbe

| Layout | Written MiB | Cancelled MiB | Net written MiB | Read MiB | Seconds | Apparent MiB | Allocated MiB |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| baseline | 2088.823 | 0.000 | 2088.823 | 1024.882 | 4.267 | 1345.273 | 1081.473 |
| lazy | 2088.823 | 0.000 | 2088.823 | 1022.596 | 3.486 | 1345.273 | 1049.477 |
| nodiscard | 2088.823 | 0.000 | 2088.823 | 1022.579 | 3.634 | 1345.273 | 1081.473 |
| lazy-nodiscard | 2088.823 | 0.000 | 2088.823 | 1022.581 | 3.504 | 1345.273 | 1049.477 |
| small-journal | 2088.822 | 0.000 | 2088.822 | 1022.599 | 3.471 | 1345.273 | 1049.477 |
| no-journal | 2088.823 | 0.000 | 2088.823 | 1022.598 | 3.464 | 1345.273 | 1049.477 |
| compact | 2088.758 | 0.000 | 2088.758 | 1022.581 | 3.515 | 1121.730 | 1049.430 |
| inodes | 2088.780 | 0.000 | 2088.780 | 1022.600 | 3.606 | 1121.730 | 1049.453 |
| tmpfs | 1049.415 | 0.000 | 1049.415 | 1022.358 | 3.383 | 1345.273 | 1081.473 |
| prezeroed | 2088.824 | 0.000 | 2088.824 | 1023.930 | 3.645 | 1345.273 | 1049.477 |
| tmpfs-prezeroed | 1049.419 | 0.000 | 1049.419 | 1022.405 | 2.798 | 1345.273 | 1049.477 |
| no-fsync | 2088.823 | 0.000 | 2088.823 | 1024.021 | 3.418 | 1345.273 | 1081.473 |
| tar-no-same | 2088.822 | 0.000 | 2088.822 | 1025.711 | 3.531 | 1345.273 | 1081.473 |

### Phase IO and wall time

Each cell is **net written MiB / read MiB / seconds**, averaged over three runs.

| Image / layout | Unpack | Estimate | Mkfs | Link | Cleanup | Conversion sync |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| alpine / baseline | 7.699 / 11.871 / 0.030 | 0.000 / 0.000 / 0.001 | 8.254 / 0.747 / 0.037 | 0.000 / 0.000 / 0.000 | -7.699 / 0.000 / 0.008 | 0.000 / 0.039 / 0.004 |
| alpine / prezeroed | 7.699 / 10.008 / 0.025 | 0.000 / 0.000 / 0.001 | 8.254 / 0.746 / 0.032 | 0.000 / 0.000 / 0.000 | -7.699 / 0.000 / 0.008 | 0.000 / 0.039 / 0.003 |
| alpine / tmpfs-prezeroed | 0.000 / 9.977 / 0.015 | 0.000 / 0.000 / 0.001 | 8.254 / 0.746 / 0.033 | 0.000 / 0.000 / 0.000 | 0.000 / 0.000 / 0.001 | 0.000 / 0.039 / 0.003 |
| ubuntu / baseline | 80.965 / 81.934 / 0.120 | 0.000 / 0.000 / 0.010 | 84.383 / 0.747 / 0.222 | 0.000 / 0.000 / 0.000 | -80.965 / 0.000 / 0.052 | 0.000 / 0.039 / 0.008 |
| ubuntu / prezeroed | 80.965 / 80.120 / 0.123 | 0.000 / 0.000 / 0.013 | 84.383 / 0.746 / 0.216 | 0.000 / 0.000 / 0.000 | -80.965 / 0.000 / 0.054 | 0.000 / 0.039 / 0.008 |
| ubuntu / tmpfs-prezeroed | 0.000 / 79.038 / 0.072 | 0.000 / 0.000 / 0.009 | 84.383 / 0.746 / 0.207 | 0.000 / 0.000 / 0.000 | 0.000 / 0.000 / 0.012 | 0.000 / 0.039 / 0.004 |
| rbe / baseline | 1039.406 / 1024.076 / 1.025 | 0.000 / 0.000 / 0.039 | 1049.417 / 0.768 / 2.812 | 0.000 / 0.000 / 0.000 | 0.000 / 0.000 / 0.374 | 0.000 / 0.038 / 0.017 |
| rbe / prezeroed | 1039.406 / 1023.116 / 0.948 | 0.000 / 0.000 / 0.046 | 1049.418 / 0.776 / 2.255 | 0.000 / 0.000 / 0.000 | 0.000 / 0.000 / 0.380 | 0.000 / 0.038 / 0.016 |
| rbe / tmpfs-prezeroed | 0.000 / 1021.602 / 0.744 | 0.000 / 0.000 / 0.039 | 1049.419 / 0.764 / 1.920 | 0.000 / 0.000 / 0.001 | 0.000 / 0.000 / 0.089 | 0.000 / 0.039 / 0.005 |

### Conversion plus sparse chunk creation

| Image / layout | Total written MiB | Total cancelled MiB | Total net MiB | Total read MiB | Seconds | Chunk allocated MiB |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| alpine / baseline | 24.527 | 7.699 | 16.828 | 12.658 | 0.094 | 8.574 |
| alpine / lazy | 24.203 | 7.699 | 16.504 | 10.760 | 0.090 | 8.250 |
| alpine / nodiscard | 24.527 | 7.699 | 16.828 | 12.547 | 0.085 | 8.574 |
| alpine / lazy-nodiscard | 24.203 | 7.699 | 16.504 | 10.762 | 0.085 | 8.250 |
| alpine / small-journal | 24.203 | 7.699 | 16.504 | 10.762 | 0.082 | 8.250 |
| alpine / no-journal | 24.195 | 7.699 | 16.496 | 10.762 | 0.081 | 8.246 |
| alpine / compact | 24.195 | 7.699 | 16.496 | 10.762 | 0.088 | 8.246 |
| alpine / inodes | 24.195 | 7.699 | 16.496 | 10.762 | 0.086 | 8.246 |
| alpine / tmpfs | 16.828 | 0.000 | 16.828 | 10.673 | 0.067 | 8.574 |
| alpine / prezeroed | 24.203 | 7.699 | 16.504 | 10.793 | 0.081 | 8.250 |
| alpine / tmpfs-prezeroed | 16.504 | 0.000 | 16.504 | 10.762 | 0.064 | 8.250 |
| alpine / no-fsync | 24.523 | 7.699 | 16.824 | 10.919 | 0.095 | 8.574 |
| ubuntu / baseline | 249.727 | 80.965 | 168.762 | 82.720 | 0.508 | 84.379 |
| ubuntu / lazy | 249.727 | 80.965 | 168.762 | 80.000 | 0.527 | 84.379 |
| ubuntu / nodiscard | 249.727 | 80.965 | 168.762 | 80.000 | 0.489 | 84.379 |
| ubuntu / lazy-nodiscard | 249.727 | 80.965 | 168.762 | 80.000 | 0.469 | 84.379 |
| ubuntu / small-journal | 249.727 | 80.965 | 168.762 | 80.000 | 0.503 | 84.379 |
| ubuntu / no-journal | 249.719 | 80.965 | 168.754 | 80.000 | 0.526 | 84.375 |
| ubuntu / compact | 249.719 | 80.965 | 168.754 | 80.000 | 0.495 | 84.375 |
| ubuntu / inodes | 249.719 | 80.965 | 168.754 | 80.000 | 0.513 | 84.375 |
| ubuntu / tmpfs | 168.762 | 0.000 | 168.762 | 79.823 | 0.395 | 84.379 |
| ubuntu / prezeroed | 249.727 | 80.965 | 168.762 | 80.905 | 0.498 | 84.379 |
| ubuntu / tmpfs-prezeroed | 168.762 | 0.000 | 168.762 | 79.823 | 0.400 | 84.379 |
| ubuntu / no-fsync | 249.723 | 80.965 | 168.758 | 80.921 | 0.490 | 84.379 |
| rbe / baseline | 3138.210 | 0.000 | 3138.210 | 1024.882 | 5.135 | 1049.387 |
| rbe / lazy | 3138.210 | 0.000 | 3138.210 | 1022.596 | 4.328 | 1049.387 |
| rbe / nodiscard | 3138.210 | 0.000 | 3138.210 | 1022.579 | 4.454 | 1049.387 |
| rbe / lazy-nodiscard | 3138.210 | 0.000 | 3138.210 | 1022.581 | 4.383 | 1049.387 |
| rbe / small-journal | 3138.208 | 0.000 | 3138.208 | 1022.599 | 4.303 | 1049.387 |
| rbe / no-journal | 3138.210 | 0.000 | 3138.210 | 1022.598 | 4.219 | 1049.387 |
| rbe / compact | 3138.078 | 0.000 | 3138.078 | 1022.581 | 4.415 | 1049.320 |
| rbe / inodes | 3138.124 | 0.000 | 3138.124 | 1022.600 | 4.705 | 1049.344 |
| rbe / tmpfs | 2098.802 | 0.000 | 2098.802 | 1022.358 | 4.238 | 1049.387 |
| rbe / prezeroed | 3138.211 | 0.000 | 3138.211 | 1023.930 | 4.863 | 1049.387 |
| rbe / tmpfs-prezeroed | 2098.806 | 0.000 | 2098.806 | 1022.405 | 3.628 | 1049.387 |
| rbe / no-fsync | 3138.210 | 0.000 | 3138.210 | 1024.021 | 4.362 | 1049.387 |

## What writes zeros, and what preserves holes?

On this host, mke2fs's regular-file zeroing uses `fallocate(FALLOC_FL_ZERO_RANGE)`, **not** a stream of zero-page writes. The default journal initialization therefore consumes allocated disk space as unwritten extents, with little corresponding `write_bytes`. Inode-table zeroing also uses zero-range operations when needed; discard/zero knowledge can eliminate table initialization. There are additional small zero ranges, including the end of the image. The payload and populated inode/bitmap blocks still have to be written. It does not fill all unused image space.

On an identical 32 MiB metadata fixture, baseline and prezeroed both issued **18 pwrite64 calls / 66,560 bytes**, **7 pread64 calls**, and **4 fsync calls**. Prezeroed reduced fallocate calls from **6 to 2**. `strace` also confirmed that GNU tar extraction issued **no fsync/fdatasync**. mke2fs's final two fsyncs remained present in the selected implementation. `nodiscard` alone did not reduce written bytes or allocation here. Lazy journal initialization and prezeroed initialization had similar space/IO results, but prezeroed also marks already-zero inode tables initialized, avoiding guest lazy initialization. Lazy inode initialization only saves writes where table zeroing would otherwise occur, and can move those writes to a later writable mount.

The sparse-image path was traced through the repository:

- `ociconv.createExt4Image`: legacy hashing reads the logical image and rename preserves holes; SHA-256 does not allocate blocks. Smaller *apparent* images would reduce this hashing CPU/zero-byte processing, but do not automatically reduce physical reads.
- `filecache.addFileWithKeyPrefix` / `cloneOrLink`: Linux `fastcopy.FastCopy` uses `os.Link`; optional cloning uses FICLONE reflink. Neither writes logical zeros. Unsupported reflinking returns an error rather than silently using a dense copy.
- `copy_on_write.ConvertFileToCOW`: truncates chunks to logical size, seeks data/hole ranges, copies only data, and omits all-hole chunks. Timed chunk allocation stayed near populated data size rather than full image size. **Unwritten zero-range extents count as holes once flushed**, explaining why baseline's larger allocated journal did not become an equivalent chunk-write cost.
- COW dirty-copy creation truncates a new chunk and skips all-zero blocks when copying its predecessor (`copyChunkIfNotDirty`). Guest writes can naturally materialize blocks; hole preservation does not eliminate real workload writes.
- `snaploader.cacheChunkedFile`: reads logical chunks to hash them, omits all-zero chunks, and caches local chunk files through AddFile. Hashing/mmap reads do not allocate holes. Local snapshot reuse uses FastLinkFile, retaining sparseness.
- **Remote snapshot fetch can de-sparsify partially populated chunks**: `snaputil.GetArtifact` creates a normal file and passes it to `cachetools.GetBlob`; the decompressor writes the complete logical byte stream, with no sparse writer. Entirely absent chunks remain absent, but holes *inside fetched chunks* become allocated zeros. This is a separate future optimization; no live remote-transfer measurements or remote writer changes were made here.

## Journal usage and compatibility

The filesystem is **not universally read-only**:

- When both local and remote chunked snapshot sharing are disabled, `firecracker.go` attaches `containerfs` with `IsReadOnly=true`; `goinit` mounts it with `MS_RDONLY` as `/container`. Writable scratchfs is the overlay upper directory, and workspace is a separate writable ext4 disk.
- Chunked snapshot sharing enables the merged `rootfs` drive, attached read-write. `goinit -enable_rootfs` mounts it with `MS_NOATIME` **without MS_RDONLY** and issues `EXT4_IOC_RESIZE_FS`. Host `resizeRootfs` extends the COW store; guest writes are isolated from the immutable cached image by COW. `snaputil.EnableLocalSnapshotSharing` defaults to **true**, so this writable merged mode is especially relevant.
- `vmexec` mounts workspace ext4 read-write with `MS_NOATIME`; its Sync RPC and execution paths call `unix.Sync`. `MakeEmptyImage` creates writable scratch storage. Removing journals from these mutable disks changes crash/recovery behavior.

The no-journal experiment passed mounted-tree comparison, writable mounting and online growth on this VM's **6.12.93 kernel**; it was not a guest Firecracker kernel test. It saved only one or a few dirty metadata blocks relative to lazy/prezeroed initialization on these inputs. A smaller journal likewise had almost no IO benefit because its zero extents were already sparse/unwritten. Retaining the journal avoids a recovery-policy change across writable guest modes for negligible byte cost. No new fs feature, journal policy or guest mount behavior is introduced by the selected flag.

## Sizing and inode choices

[The sizing probe](enterprise/tools/ext4_bench/sizing.py) searched in 1 MiB increments, using the verified baseline tree as input (including its synthetic lost+found), with the journal retained and lazy/nodiscard enabled. This is an **untimed space-feasibility probe**, not part of the three-repeat IO matrix. Trial success means mke2fs completed; minimum layouts are not proposed as production defaults. Metadata-heavy or xattr-heavy inputs outside this sample can require different slack.

| Image | Current estimate MiB | Current auto image MiB | Compact image MiB | Minimum with default inodes MiB | Minimum with counted inodes MiB | Minimum `-i 65536` MiB | Minimum `-i 131072` MiB |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Alpine | 10.102 | 16.121 | 14.102 | 13 | 13 | 33 | 65 |
| Ubuntu | 100.840 | 125.008 | 104.840 | 94 | 89 | 215 | 425 |
| RBE | 1117.730 | 1345.273 | 1121.730 | 1096 | 1083 | 1083 | 2075 |

The estimator already charges 4 KiB of metadata per entry, another block per directory, rounds every regular file up to blocks, and overcounts hardlinked data. In these samples **1.0 × estimate + 4 MiB was sufficient**, including a journal; the extra 20% was unnecessary. However, dropping it saved only about **4 KiB, 4 KiB, and 67 KiB** of conversion writes respectively (small run-to-run counter differences aside). Journal/inode tables can already be sparse; populated file metadata remains necessary. Counted inodes did not give a material additional IO reduction. Fixed high `-i` ratios can run out of inodes and require **larger** images: they are particularly poor for small-file images. Counting inodes also determines future writable-growth inode availability. Retain the current conservative sizing policy until a broader workload study justifies changing it.

## Page cache, fsync, and tar flags

For Alpine and Ubuntu, deletion cancelled essentially **all unpack data writes** (7.699 and 80.965 MiB). Thus the claim that every conversion necessarily puts twice the rootfs bytes on storage is false for fast conversions with adequate cache. Page-cache reads also meant mke2fs did not reread the whole unpack tree from storage. On the large image, **zero unpack bytes were cancelled**: dirty-background writeback had already sent the 1,039.406 MiB unpack tree to disk. Timing/concurrency/dirty thresholds and registry download speed will change this balance in production.

The no-fsync interposer suppressed only mke2fs fsync/fdatasync calls, then the benchmark deleted unpack and ran `sync`. It did **not** materially reduce persisted bytes for any image; the large unpack tree had reached disk anyway. Its time improvement mostly rearranges flush work and is not sufficient evidence to weaken durability. mke2fs's `MKE2FS_SYNC` setting controls periodic inode-table syncs, not the final fsync guarantee. No fsync behavior was changed in production.

`tar --no-same-owner --no-same-permissions` did not materially reduce byte counts, and changed **4 Alpine, 29 Ubuntu, and 47 RBE tree entries**. Examples: Alpine `/etc/shadow` lost GID 42, `/tmp` and `/var/tmp` lost sticky/writable bits, and Ubuntu `/home/ubuntu` changed UID/GID 1000 to 0. It fails the task's metadata-preservation requirement and is rejected.

## Empty/workspace/scratch disks

`MakeEmptyImage` uses the same zero-initialization behavior. Three cold-cache runs of each layout, at each size, produced these means; all outputs passed `e2fsck -fn`:

| Apparent MiB | Layout | Written MiB | Cancelled MiB | Read MiB | Seconds | Allocated MiB |
| ---: | --- | ---: | ---: | ---: | ---: | ---: |
| 256 | baseline | 0.121 | 0.000 | 1.978 | 0.026 | 16.180 |
| 256 | lazy-nodiscard | 0.121 | 0.000 | 1.935 | 0.017 | 0.184 |
| 256 | prezeroed | 0.121 | 0.000 | 1.936 | 0.017 | 0.184 |
| 1024 | baseline | 0.336 | 0.000 | 1.938 | 0.015 | 32.395 |
| 1024 | lazy-nodiscard | 0.336 | 0.000 | 1.979 | 0.016 | 0.398 |
| 1024 | prezeroed | 0.336 | 0.000 | 1.938 | 0.048 | 0.398 |

These save journal zero-extent allocation (16 MiB and 32 MiB here), not large dirty-page writes on this host. Workspace directory images also use DirectoryToImage, so the prezeroed flag applies there without removing the journal or reducing writable slack. An empty prezeroed filesystem and a populated one both passed fsck with the journal retained; the metadata fixture passed writable mounting, data creation, and online growth from 256 to 512 MiB.

## Correctness, validation, and limits

- **108** primary timed images (12 layouts × 3 images × 3 runs): each passed `e2fsck -fn`, loop read-only mounting, and canonical tree comparison with baseline. Manifest comparison includes file SHA-256, modes/types, uid/gid, symlinks, hardlink groups, character/block device numbers, xattrs including symlink xattrs, and nanosecond mtimes; root/lost+found are included. Disk and tmpfs synthetic fixture checks additionally covered a setuid file, UID/GID changes, a security.capability xattr, user file/directory xattrs, FIFO, character and block nodes, dangling symlink, and hardlink.
- **9** tar-no-same runs measured as negative results with verification deferred. Each first output passed fsck/mount but failed tree comparison; all differing entries were inspected. **18** empty-image outputs passed fsck.
- `bazel test //enterprise/server/util/ext4/... //enterprise/server/oci/ociconv/...` passed with **e2fsprogs 1.47.3**. The pre-existing reproducibility test needs 1.47.2+ and failed with the VM's original 1.47.0, independently of this optimization. Both reproducible and prezeroed flag combinations are now covered. The new prezeroed image tests also passed directly with the original 1.47.0 tools. The original tools were restored for all final IO measurements.
- Root execution of the conversion tests exercised actual successful tmpfs mounting and size-exhaustion retry; ordinary nonroot tests cover lack-of-mount-permission fallback. Added tests cover compressed-size rejection, default disk path, tmpfs success/fallback, cleanup on cancellation and mkfs failure, retained output protection, sparse populated/empty images, journal retention, content/links, fsck, and actual filecache conversion/linking with both flag values.
- `bazel build //enterprise/tools/ext4_bench` passed, including repository static analysis. Reproduction, empty-image, sizing and compatibility scripts are committed alongside raw results.

Risks/limits: prezeroed initialization is valid only for fresh/empty regular files; it must never be generalized to reused nonzero files or raw devices without proving storage is zero. Older e2fsprogs may not support the option; it was exercised on 1.47.0 and 1.47.3. Tmpfs needs mount privileges and consumes memory **per concurrent conversion**; its size bound does not bound aggregate executor memory or prevent OOM. Compressed-size filtering is conservative and cannot predict flattened size, so an underestimated tree can pay for a failed extraction followed by another registry stream/decompression. tmpfs versus disk host security labels (e.g. SELinux) may differ; validate xattrs on target hosts before enabling that flag. Sparse allocation results depend on the host filesystem's zero-range and hole support. Filesystem/device IO may exceed process counters due to journal/metadata writeback. Three runs on one idle VM establish the byte behavior observed here, not production latency guarantees or Firecracker-kernel compatibility across versions. Remote snapshot restore remains a known source of chunk densification.
