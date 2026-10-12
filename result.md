Two-pass `mke2fs -d -` can eliminate the unpack directory and roughly halve gross conversion writes. On the 1.057 GB RBE rootfs it reduced writes from 2089 MiB to 1051 MiB, but increased conversion time from 9.9 s to 15.0 s. It also changed 85 directory mtimes relative to GNU tar extraction. The executor prototype is therefore **disabled by default** and rejects non-contiguous directory subtrees before writing an image. It converts Alpine and Ubuntu correctly; it intentionally refuses this RBE image. Keep the flag off for general use.

The pure-Go writer was faster (6.7 s for RBE) and issued far fewer write syscalls, but all its images failed `e2fsck -fn`. It also retained archive xattrs that baseline omits, preserved fractional mtimes that baseline truncates, and expanded sparse files into allocated zero blocks. It is a promising follow-up after those problems are addressed, rather than a replacement to deploy now.

Implementation and approaches

`executor.stream_ext4_images`, an internal bool defaulting to false, selects `ext4.TarToImageAutoSize`. Its first pass estimates space from flattened tar headers, including implicit parents and block-rounded file payloads; it does not walk an unpack tree. It counts metadata conservatively (8 KiB per path), adds 20% plus 4 MiB, and requests enough inodes explicitly. This differs from baseline's inode/metadata estimate and double-counting of hardlinked contents. Filesystem apparent size and free space are consequently not byte-identical to baseline.

The second pass normalizes the flattened stream into an OS pipe connected to `mke2fs -d -`. File contents are copied with a 256 KiB output buffer; only names, inode metadata, directory headers, and deferred hardlinks are retained. Parents can occur after children in `mutate.Extract`, and hardlinks can reference later entries, so forwarding the stream without normalization is unsafe for arbitrary images. Hardlink headers use target metadata because GNU tar ignores differing link-header metadata, while mke2fs applies it to the shared inode. Archive xattrs are removed to match current `tar --extract` without `--xattrs`. `/dev/*` exclusion and non-empty-output protection remain supported. Cancellation closes source readers; an early child exit closes its input pipe; source errors after tar EOF are drained and propagated. Failed partial images are removed. The reproducible-images flag combination is rejected explicitly.

GNU tar restores directory metadata when leaving a subtree. If a flattened archive revisits that subtree later, creating children resets some directory mtimes to extraction time. Writers that restore all directory headers at the end preserve archive times instead. The executor's sizing pass conservatively rejects such revisits; it does not silently fall back to unpacking. The unrestricted benchmark variant remains available to quantify the potential I/O savings and the incompatibility. The guard can reject safe revisits too. It is not a complete emulation of GNU tar for every possible malformed archive, host labeling policy, or extraction privilege configuration.

Source review and direct tests confirmed that e2fsprogs 1.47.1 introduced tar population. `populate_fs2` explicitly recognizes `"-"` and passes NULL to libarchive to read stdin. **A pipe works without seeking or a tar file on disk.** A FIFO can feed stdin in the same way. A FIFO pathname passed directly as `-d /path/fifo` is not supported: mke2fs only dispatches `-` or regular files to its tar reader; the FIFO pathname fails with `Not a directory while changing working directory`. `/proc/self/fd/0` has the same file-type dispatch limitation for pipe input. The direct FIFO-path failure was reproduced, and every mke2fs streaming benchmark used a real pipe.

`dockerfiles/executor_image/Dockerfile` uses apt suites `trixie`, despite the older base-image comment. Packages are not pinned; Debian currently lists e2fsprogs 1.47.2-3 (with architecture-specific rebuild suffixes). Thus the source Dockerfile selects a tar-capable release for new builds; a deployed executor image was not inspected. Production uses `/sbin/mke2fs`; no Bazel-provided mke2fs target was found. `libarchive13t64` is only a Debian package suggestion, so this change installs it explicitly in the executor image. The VM initially shipped mke2fs 1.47.0. Experiments used a locally built upstream 1.47.2 with runtime libarchive 3.7.2; package tests used the same binary temporarily at `/sbin/mke2fs`.

The libarchive implementation supports hardlinks, symlinks, device nodes, long PAX names, and sparse data (zero filesystem blocks are skipped). It copies only `security.capability` in 1.47.1, plus `gnu.translator` in 1.47.2; it is **not a general xattr-preserving tar importer**. Normalization deliberately drops all archive xattrs because the current baseline does. General xattr support would need a separate change to both baseline and tar import. On SELinux hosts, baseline may acquire host-assigned labels when extracting; equivalence for those hosts was not established.

`github.com/Microsoft/hcsshim/ext4/tar2ext4` is MIT-licensed and pure Go. v0.13.0 was tried initially; final experiments use the latest stable v0.14.1. Its compact writer implementation is unchanged between those releases and the inspected main branch. `ConvertTarToExt4` needs a seekable **output**, but accepts a non-seekable input and determines final filesystem size at close. `MaximumDiskSize` reserves descriptor capacity rather than requiring an exact rootfs size; default is 16 GiB, maximum is 16 TiB, and individual files are limited to 128 GiB. It uses a 512 KiB buffered output writer, plus per-inode/path/xattr/directory metadata and extent bookkeeping in memory. It does not retain whole large-file payloads or create temporary input files. The separate `ConvertAndComputeRootDigest` helper does create a temporary image, but was not used. Input buffering and decompression add their own bounded buffers. The mke2fs tar reader uses a 16 MiB copy buffer, in addition to libarchive buffers and normalizer/path metadata. Metadata memory is O(number of paths), not O(rootfs bytes); no enforced metadata-memory cap was added.

Observed filesystem features (`dumpe2fs -h`):

| Property | Directory baseline and mke2fs tar | hcsshim default |
|---|---|---|
| Type / block / inode size | ext4 / 4096 / 256 | ext4 / 4096 / 256 |
| Journal | enabled when image is large enough | absent |
| Extents / 64bit | extents / disabled by `-O ^64bit` | extents / absent |
| Inline data | absent | absent by default; optional `InlineData` |
| Compatibility features | has_journal, ext_attr, resize_inode, dir_index, orphan_file, filetype, extent, flex_bg, metadata_csum_seed, sparse_super, large_file, huge_file, dir_nlink, extra_isize, metadata_csum | ext_attr, sparse_super2, filetype, extent, flex_bg, large_file, huge_file, extra_isize, read-only |

mke2fs features also depend on host `mke2fs.conf`; the table records this VM's configuration. The streaming path uses the same ext4 options as existing code, apart from explicit inode count and tar input. The repo has Firecracker guest kernel configs for 5.10, 5.15, and 6.1, all with built-in ext4, ACLs, and security support. `enterprise/server/cmd/goinit/main.go` mounts `/container` with `MS_RDONLY`. The hcsshim feature set is supported for read-only mounting by these kernel families; 5.10 already defines inline-data and read-only feature support. Absence of a journal is acceptable for that read-only lower filesystem, but hcsshim's read-only feature makes it unsuitable as a writable workspace image. Host loop mounting was tested; a Firecracker guest boot was not run. Existing mke2fs feature compatibility (including host-selected orphan_file) was not changed.

A 128 MiB tmpfs was also measured for Alpine and Ubuntu on this no-swap VM. It preserves the baseline extraction/read-back steps but removes their disk data writes. It can retain the baseline tree semantics without requiring libarchive, at the cost of bounded, non-reclaimable unpack memory while conversion runs. RBE exceeds the budget and was not run in tmpfs. Any production adoption would need an admission/memory budget and a disk fallback; no tmpfs production behavior was added.

Other sizing choices considered: OCI layer descriptors report compressed sizes and diff-ID hashes, not flattened file sizes or inode counts, so they are not a reliable ext4 capacity estimate. A disk tar spool avoids per-file unpack operations but still writes and reads a full tar. A tmpfs tar spool trades a whole-rootfs memory allocation for one decompression pass. mke2fs fixes superblock/block-group sizes up front; merely using a sparse output and truncating it afterwards would corrupt the filesystem. An oversized image followed by `resize2fs -M` adds another filesystem traversal and I/O. The pure-Go growable writer is the cleanest single-pass sizing mechanism, once correctness is fixed. Two passes may fetch registry layers twice in production if the image readers are not backed by a local layer cache; the local-cache benchmark below does not include that network penalty.

Measurement

Environment: exe.dev VM, Ubuntu 24.04, Linux 6.12.93, 2 vCPUs, 7.7 GiB RAM, no swap, a 25 GiB `/dev/vda` filesystem, Go 1.27.2, GNU tar 1.35. All final configurations were run three times. Builds and registry downloads completed before final timing. `sync` and `echo 3 > /proc/sys/vm/drop_caches` were performed before **every** run. There was no sync between extraction/population/deletion: this preserves the cancellation of dirty unpack writes. A final sync, excluded from time/counters, stabilized `st_blocks`. The initial exploratory runs that synced between phases were discarded from the final results.

The standalone tool fetches real images once into a local OCI layout and reports that phase separately, then times cold compressed-layer conversion. It reports `/proc/self/io` for itself and waited children, with `Getrusage(SELF)+Getrusage(CHILDREN)` as a cross-check. Every recorded phase's read/write byte counter matched rusage blocks times 512. `write_bytes` includes dirtied pages that may subsequently be discarded, so `cancelled_write_bytes` is shown separately. Written-minus-cancelled is an estimate of surviving charged writes, not a device-level accounting of metadata, journaling, or request merging. Write syscall counts include pipes and are not physical disk-operation counts. Apparent/allocated sizes were confirmed with `du -B1 --apparent-size` / `du -B1` and match `st_size` / `st_blocks*512`.

Default finalization models the shared filecache's hardlink, rather than instantiating its LRU/metrics machinery. It does not hash image contents. The tool also supports legacy hashing (`--finalize legacy`); that mode was not part of the primary runs. No measurements claim to include cold network resolution, cache-hit lookup, or a deployment's optional clone behavior.

Immutable image manifests used (linux/amd64):

| Alias | Reference | Manifest digest | Flattened regular payload bytes | Tar entries |
|---|---|---|---:|---:|
| Alpine | mirror.gcr.io/library/alpine:3.20 | sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e | 7,802,737 | 517 |
| Ubuntu | mirror.gcr.io/library/ubuntu:24.04 | sha256:f610ab94648195aa356059f5b41d6085c9d4d903c072430cdd1af7bdb646106b | 78,176,728 | 3443 |
| RBE | gcr.io/flame-public/rbe-ubuntu24-04:latest | sha256:5d2d8b3935c4dad4d81e0b6d89eeb06734d0e708cdf3d8ec779cf619c9243860 | 1,056,880,036 | 16758 |

RBE's flattened payload exceeds 1 GB (decimal); its baseline apparent image is 1.411 GB. Initial registry fetches wrote 3.65 / 29.79 / 398.39 million bytes and took 3.12 / 3.57 / 6.18 seconds respectively, separate from the conversion totals.

Median conversion results follow; units are MiB (2^20 bytes) and seconds. `mke2fs` is the unrestricted prototype; `mke2fs-strict` is the flag-selected executor path. A successful conversion is not a successful correctness check; see the correctness table below. All rows have three runs. RBE strict rejects before output creation, so its zero image size means no image was produced.

| Image | Variant | Runs | Written MiB | Read MiB | Cancelled MiB | Seconds | Apparent MiB | Allocated MiB | Write syscalls | Result |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| Alpine | baseline | 3 | 16.08 | 14.58 | 7.70 | 0.124 | 16.12 | 12.50 | 4029 | converted |
| Alpine | mke2fs | 3 | 8.38 | 50.36 | 0.00 | 0.204 | 18.09 | 12.47 | 3273 | converted |
| Alpine | mke2fs-raw | 3 | 8.38 | 44.04 | 0.00 | 0.172 | 18.09 | 12.47 | 3413 | converted |
| Alpine | hcsshim | 3 | 8.23 | 3.48 | 0.00 | 0.067 | 8.23 | 8.23 | 19 | converted |
| Alpine | hcsshim-normalized | 3 | 8.23 | 3.48 | 0.00 | 0.078 | 8.23 | 8.23 | 19 | converted |
| Alpine | tmpfs | 3 | 8.38 | 8.00 | 0.00 | 0.128 | 16.12 | 12.50 | 4016 | converted |
| Alpine | mke2fs-strict | 3 | 8.38 | 51.07 | 0.00 | 0.187 | 18.09 | 12.47 | 3056 | converted |
| Ubuntu | baseline | 3 | 165.47 | 33.46 | 80.96 | 0.950 | 125.01 | 88.57 | 43952 | converted |
| Ubuntu | mke2fs | 3 | 84.53 | 68.96 | 0.00 | 1.387 | 133.45 | 100.59 | 37833 | converted |
| Ubuntu | mke2fs-raw | 3 | 84.53 | 69.09 | 0.00 | 1.318 | 133.45 | 100.59 | 40053 | converted |
| Ubuntu | hcsshim | 3 | 84.43 | 28.52 | 0.00 | 0.644 | 84.43 | 84.43 | 171 | converted |
| Ubuntu | hcsshim-normalized | 3 | 84.43 | 28.52 | 0.00 | 0.641 | 84.43 | 84.43 | 171 | converted |
| Ubuntu | tmpfs | 3 | 84.51 | 33.05 | 0.00 | 0.883 | 125.01 | 88.57 | 43718 | converted |
| Ubuntu | mke2fs-strict | 3 | 84.53 | 69.67 | 0.00 | 1.382 | 133.45 | 100.59 | 36555 | converted |
| RBE | baseline | 3 | 2089.15 | 384.88 | 0.00 | 9.921 | 1345.27 | 1081.80 | 469157 | converted |
| RBE | mke2fs | 3 | 1050.69 | 420.43 | 0.00 | 15.003 | 1408.00 | 1081.84 | 430755 | converted |
| RBE | mke2fs-raw | 3 | 1050.07 | 420.64 | 0.00 | 14.809 | 1408.00 | 1081.84 | 383127 | converted |
| RBE | hcsshim | 3 | 1052.70 | 380.20 | 0.00 | 6.707 | 1052.70 | 1052.70 | 2109 | converted |
| RBE | hcsshim-normalized | 3 | 1052.70 | 380.20 | 0.00 | 7.053 | 1052.70 | 1052.70 | 2111 | converted |
| RBE | mke2fs-strict | 3 | 0.00 | 0.15 | 0.00 | 0.002 | 0.00 | 0.00 | 0 | ERROR |


Small images' unpack writes were almost entirely cancelled (7.70 MiB for Alpine and 80.96 MiB for Ubuntu). Consequently, mke2fs's lower **gross** writes do not substantially reduce surviving writes there. On RBE no unpack writes were cancelled; streaming saves about 1 GiB of charged writes. mke2fs reads about 35 MiB more cold disk data for RBE and Ubuntu, largely from dynamically loading libarchive and its dependencies (including ICU); two-pass decompression and normalization increase CPU/time. Compressed blobs fit in cache here, so the second pass largely avoids additional physical layer reads. Under memory pressure that benefit can disappear. The pure-Go writer avoids both unpacking and the cold libarchive load.

The mke2fs estimate makes Ubuntu cross the 128 MiB journal-size threshold: its allocated image rises from 88.57 to 100.59 MiB although file contents match. This is another reason not to promote the conservative estimator unchanged. RBE's write syscall count falls only about 8% with normalized mke2fs (469157 to 430755), versus roughly 99.5% with buffered pure Go (2109). Syscall reduction is distinct from physical disk-request reduction.

Per-phase medians (zero/tiny finalize and size times round to 0.000):

| Image | Variant | Phase | Written MiB | Read MiB | Cancelled MiB | Seconds |
|---|---|---|---:|---:|---:|---:|
| Alpine | baseline | extract | 7.70 | 12.04 | 0.00 | 0.078 |
| Alpine | baseline | size | 0.00 | 0.00 | 0.00 | 0.002 |
| Alpine | baseline | populate | 8.38 | 2.41 | 0.00 | 0.034 |
| Alpine | baseline | cleanup | 0.00 | 0.00 | 7.70 | 0.007 |
| Alpine | baseline | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Alpine | mke2fs | size-pass | 0.00 | 3.48 | 0.00 | 0.061 |
| Alpine | mke2fs | populate | 8.38 | 40.57 | 0.00 | 0.144 |
| Alpine | mke2fs | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Alpine | mke2fs-raw | size-pass | 0.00 | 3.48 | 0.00 | 0.055 |
| Alpine | mke2fs-raw | populate | 8.38 | 40.57 | 0.00 | 0.120 |
| Alpine | mke2fs-raw | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Alpine | hcsshim | populate | 8.23 | 3.48 | 0.00 | 0.067 |
| Alpine | hcsshim | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Alpine | hcsshim-normalized | populate | 8.23 | 3.48 | 0.00 | 0.077 |
| Alpine | hcsshim-normalized | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Alpine | tmpfs | extract | 0.00 | 5.59 | 0.00 | 0.080 |
| Alpine | tmpfs | size | 0.00 | 0.00 | 0.00 | 0.001 |
| Alpine | tmpfs | populate | 8.38 | 2.41 | 0.00 | 0.051 |
| Alpine | tmpfs | cleanup | 0.00 | 0.00 | 0.00 | 0.002 |
| Alpine | tmpfs | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Alpine | mke2fs-strict | size-pass | 0.00 | 9.80 | 0.00 | 0.063 |
| Alpine | mke2fs-strict | populate | 8.38 | 41.27 | 0.00 | 0.124 |
| Alpine | mke2fs-strict | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Ubuntu | baseline | extract | 80.96 | 30.91 | 0.00 | 0.608 |
| Ubuntu | baseline | size | 0.00 | 0.00 | 0.00 | 0.011 |
| Ubuntu | baseline | populate | 84.51 | 2.41 | 0.00 | 0.253 |
| Ubuntu | baseline | cleanup | 0.00 | 0.00 | 80.96 | 0.056 |
| Ubuntu | baseline | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Ubuntu | mke2fs | size-pass | 0.00 | 28.40 | 0.00 | 0.524 |
| Ubuntu | mke2fs | populate | 84.53 | 40.57 | 0.00 | 0.854 |
| Ubuntu | mke2fs | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Ubuntu | mke2fs-raw | size-pass | 0.00 | 28.40 | 0.00 | 0.525 |
| Ubuntu | mke2fs-raw | populate | 84.53 | 40.57 | 0.00 | 0.802 |
| Ubuntu | mke2fs-raw | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Ubuntu | hcsshim | populate | 84.43 | 28.52 | 0.00 | 0.644 |
| Ubuntu | hcsshim | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Ubuntu | hcsshim-normalized | populate | 84.43 | 28.52 | 0.00 | 0.641 |
| Ubuntu | hcsshim-normalized | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Ubuntu | tmpfs | extract | 0.00 | 30.64 | 0.00 | 0.622 |
| Ubuntu | tmpfs | size | 0.00 | 0.00 | 0.00 | 0.007 |
| Ubuntu | tmpfs | populate | 84.51 | 2.41 | 0.00 | 0.218 |
| Ubuntu | tmpfs | cleanup | 0.00 | 0.00 | 0.00 | 0.016 |
| Ubuntu | tmpfs | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| Ubuntu | mke2fs-strict | size-pass | 0.00 | 28.40 | 0.00 | 0.510 |
| Ubuntu | mke2fs-strict | populate | 84.53 | 41.27 | 0.00 | 0.872 |
| Ubuntu | mke2fs-strict | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| RBE | baseline | extract | 1039.41 | 382.33 | 0.00 | 6.854 |
| RBE | baseline | size | 0.00 | 0.00 | 0.00 | 0.043 |
| RBE | baseline | populate | 1049.75 | 2.41 | 0.00 | 2.400 |
| RBE | baseline | cleanup | 0.00 | 0.00 | 0.00 | 0.395 |
| RBE | baseline | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| RBE | mke2fs | size-pass | 0.00 | 380.20 | 0.00 | 6.181 |
| RBE | mke2fs | populate | 1050.69 | 40.23 | 0.00 | 8.844 |
| RBE | mke2fs | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| RBE | mke2fs-raw | size-pass | 0.00 | 380.20 | 0.00 | 6.277 |
| RBE | mke2fs-raw | populate | 1050.07 | 40.45 | 0.00 | 8.538 |
| RBE | mke2fs-raw | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| RBE | hcsshim | populate | 1052.70 | 380.20 | 0.00 | 6.707 |
| RBE | hcsshim | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| RBE | hcsshim-normalized | populate | 1052.70 | 380.20 | 0.00 | 7.053 |
| RBE | hcsshim-normalized | finalize | 0.00 | 0.00 | 0.00 | 0.000 |
| RBE | mke2fs-strict | size-pass | 0.00 | 0.15 | 0.00 | 0.002 |


Correctness

Every completed candidate image mounted successfully with `mount -o loop,ro`. The comparator hashes regular files, uses `lstat` for modes/uid/gid/mtime and symlinks, compares hardlink equivalence classes rather than inode numbers across filesystems, compares device type and major/minor numbers, and reads all xattrs without following links. It also emits the requested `find -printf` listings. GNU `diff -r` was tried on Alpine, but its dangling-link errors and attempts to read special files make the dedicated checker authoritative. `/` and `lost+found` timestamps are recorded but exempted because they are generated per conversion; all other mtime differences fail the comparison.

| Image | Variant | Tree differences excluding generated root/lost+found times | e2fsck -fn |
|---|---|---|---|
| Alpine / Ubuntu | mke2fs, mke2fs-strict, mke2fs-raw, tmpfs | none | exit 0 |
| Alpine / Ubuntu | hcsshim, hcsshim-normalized | none | exit 4, inode-bitmap padding |
| RBE | mke2fs | 85 directory mtimes | exit 0 |
| RBE | mke2fs-raw | 85 directory mtimes + capability xattr on gst-ptp-helper | exit 0 |
| RBE | hcsshim | same 85 directory mtimes + capability xattr | exit 4, inode-bitmap padding |
| RBE | hcsshim-normalized | 85 directory mtimes | exit 4, inode-bitmap padding |
| RBE | mke2fs-strict | rejected at `etc` before output creation | no image |
| Synthetic fixture | mke2fs, mke2fs-strict | none | exit 0 |
| Synthetic fixture | mke2fs-raw | capability xattr on both names of a hardlink pair | exit 0 |
| Synthetic fixture | hcsshim | fractional mtimes on 10 paths + xattrs on hardlink pair | exit 4, inode-bitmap padding |
| Synthetic fixture | hcsshim-normalized | fractional mtimes on 10 paths | exit 4, inode-bitmap padding |

Alpine / Ubuntu / RBE compared 519 / 3445 / 16760 tree entries including root and lost+found. RBE's 85 differences are all directories: baseline timestamps fall in its extraction window, while candidates retain historical image timestamps. File hashes, ownership, modes, symlinks, hardlink groupings, and device numbers match. The extra raw-writer xattr is `security.capability` on `usr/lib/x86_64-linux-gnu/gstreamer1.0/gstreamer-1.0/gst-ptp-helper`.

The fixture includes a 32 MiB sparse file with data only at its ends, 200-character path components, a symlink, hardlinks, non-root uid/gid, suid/sgid permissions, a FIFO, a character device and block device, user/capability xattrs, and fractional mtimes. **`dev/block` was compared as metadata, never read as a block device:** baseline and mke2fs have mode 0644, uid/gid 0:0, major/minor 8:1, mtime 1600000000. Hcsshim matches the device fields but keeps mtime 1600000000.123456789, so it fails strict timestamp comparison. `dev/null` similarly matches 1:3. Baseline omits both archive xattrs; normalized mke2fs omits them too. Neither implementation can be called a transparent replacement merely because it mounts.

The sparse fixture's final baseline image allocates about 4.3 MiB, versus about 32.1 MiB with hcsshim. mke2fs skips zero blocks and preserves logical contents with sparse allocation; hcsshim writes the expanded zero payload and does not recover holes. Thus pure-Go I/O savings can reverse on sparse-heavy images. All hcsshim images report `Padding at end of inode bitmap is not set`; exit 4 was reproduced with e2fsck 1.47.0 and a built 1.47.2. This was not auto-repaired or hidden in the measurements.

Tests and recommendation

`bazel test //enterprise/server/util/ext4/... //enterprise/server/oci/ociconv/... --jobs=4 --test_output=errors` passed all three test targets, including all pre-existing tests. Added coverage includes streaming filesystem creation/content, device exclusion, modes/uid/gid/hardlinks, directory modes, symlinks, filesystem checks, overwrite protection, incompatible reproducibility flags, malformed/truncated tar, escaping paths, leading slash normalization, missing parents, forward/cyclic hardlinks, target metadata, cancellation of blocked reads, errors after tar EOF, and conservative directory-mtime rejection before creating an image. The final tarstream package also passed `go test -race`. The benchmark module builds independently using `go build`.

Keep `executor.stream_ext4_images=false` for general production use. It is a limited, guarded research path for compatible archives; layered images like RBE are refused rather than returned with changed guest metadata. It also changes capacity/inode allocation and can be slower or fetch twice. Exact GNU tar directory-restoration behavior, sizing parity, runtime labeling/privilege equivalence, and a guest boot test would be prerequisites for general rollout. For small images, budgeted tmpfs is a simpler option if memory admission and fallback are implemented. For a general one-pass converter, pursue the pure-Go writer after fixing the bitmap error, sparse allocation, timestamp/xattr adaptation, and validating the complete guest boot path. Neither candidate is currently a drop-in improvement satisfying all compatibility and performance goals across these images.

Reproduction commands and helper scripts are in [enterprise/tools/oci_ext4_benchmark/README.md](enterprise/tools/oci_ext4_benchmark/README.md). Raw per-run/per-phase byte counters, rusage cross-checks, timings, image sizes, immutable image descriptions, full mismatch records, and filesystem-check logs are committed under [enterprise/tools/oci_ext4_benchmark/results](enterprise/tools/oci_ext4_benchmark/results). Generated full `find` listings are retained in the validation output and can be recreated with `validate.sh`; they are not checked in. The research-only hcsshim dependency is isolated in that tool's nested Go module.

Compatibility and subprocess cleanup

Streaming normalization sets symlink modes to 0777, matching Linux GNU tar even when the archive records restrictive permissions. Synthetic implicit parents use 0777 masked by the process umask. The mask is read from Linux /proc/self/status without changing process-global state; conversion fails explicitly if it cannot read or parse the mask. This requires procfs exposing Umask (Linux 4.7+). Explicit directory headers restore archived permissions. An isolated-subprocess comparison under umask 077 verifies symlink and implicit/explicit directory permissions against baseline images.

Deterministic subprocess tests cover cancellation after population starts reading a blocked source and child failure before consuming the stream. They verify bounded return, child reaping/termination, source-reader closure, and removal of partial output. A private command-construction parameter substitutes controlled children without changing the real mke2fs binary or public API.

OCI conversion attempts streaming only when the executor's effective UID is zero. Non-root conversion uses GNU tar unpacking to preserve extracting-user ownership and umask semantics. Hardlinks targeting symlinks return a specific compatibility error during sizing; OCI conversion catches only that error and reopens the flattened archive for baseline extraction. Other errors propagate. The guard covers forward references, chains, and previously seen/replaced symlink entries; excluded device entries do not trigger it. Comparison tests observe actual GNU tar invocations and verify fallback ownership, modes, link targets, independent symlink inodes, and nlink 1. Unit tests cover forward/chained hardlinks and device exclusion.

Fallback incurs baseline disk I/O; hardlink compatibility fallback also incurs a preliminary sizing pass. Directory-mtime rejection and the other compatibility limits described above still apply. Streaming remains disabled by default. Firecracker guest boot validation is out of scope.

Tests added

- The OCI streaming integration test explicitly skips non-root workers, whose fallback is covered separately. Under root it replaces GNU tar with a failing sentinel, so baseline extraction cannot silently satisfy the streaming test.
- Automated baseline-versus-streaming filesystem comparisons cover an 8 MiB sparse payload (expanded zero ranges as in flattened OCI layers), GNU and PAX names longer than 100 characters, user/security.capability xattr omission, and fractional mtime truncation. They compare contents and inode metadata, bound sparse block allocation, and run e2fsck -fn on both images.
- The benchmark comparator groups every non-directory inode by device/inode identity, including symlinks and special files. Its regression test compares hardlinked symlinks and FIFOs with independently created equivalents and verifies that identical metadata cannot conceal changed hardlink relationships. FIFOs are never read.

These additions change only tests and benchmark comparison behavior; no converter behavior changes were needed. Earlier benchmark measurements were not refreshed.

Validation for the test-gap changes

The following uncached command passed all six test targets as the unprivileged VM user (including the existing fallback tests). The OCI streaming integration case explicitly skipped with its root-requirement message; all five new filesystem comparisons ran and passed.

```sh
bazel test --nocache_test_results //enterprise/server/util/ext4/... //enterprise/server/oci/ociconv/... //enterprise/tools/oci_ext4_benchmark:compare_test --jobs=4 --test_output=errors
```

A second uncached Bazel run selected `TestStreamingBaselineArchiveFeatures|TestConvertContainerToExt4FS_Streaming` in `//enterprise/server/util/ext4:stream_internal_test` and `//enterprise/server/oci/ociconv:ociconv_test`, using `--strategy=TestRunner=local` and a temporary `--run_under` launcher that invokes `sudo -n -E` and returns XML report ownership to the Bazel user. Both targets passed; the OCI streaming case ran without skipping and all five filesystem comparisons passed as root. The launcher is necessary because sudo cannot elevate inside Bazel's user namespace sandbox, and Bazel must own the generated test reports. No tmpfs mount is needed by these additions.

The VM ships mke2fs 1.47.0. Both successful runs used locally built upstream e2fsprogs 1.47.2 temporarily at /sbin/mke2fs with the system libarchive; the original binary was restored afterward. Successful uncached invocations: [full package suite](https://app.buildbuddy.io/invocation/53b7b31d-f462-4e33-a350-25f11bb4ea3c), [root comparisons and OCI streaming](https://app.buildbuddy.io/invocation/ba2f2661-2191-46e6-bc16-9acb641552c3).
