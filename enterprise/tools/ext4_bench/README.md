# ext4 conversion IO benchmark

Run on an otherwise idle Linux VM with root/mount privileges and enough disk
space for the flattened tar, unpack tree, image and chunks. All timed commands
run sequentially. The tool deliberately performs global `sync` and writes `3`
to `/proc/sys/vm/drop_caches` before **every** repetition.

```sh
bazel build //enterprise/tools/ext4_bench
# Or: go build -o /var/tmp/ext4_bench ./enterprise/tools/ext4_bench
BENCH="$PWD/bazel-bin/enterprise/tools/ext4_bench/ext4_bench_/ext4_bench"
mkdir -p /var/tmp/ext4-bench
"$BENCH" -fetch mirror.gcr.io/library/alpine:3.20 -tar /var/tmp/ext4-bench/alpine.tar
sudo "$BENCH" -tar /var/tmp/ext4-bench/alpine.tar -image alpine \
  -work /var/tmp/ext4-bench/alpine -runs 3 \
  -configs baseline,lazy,nodiscard,lazy-nodiscard,small-journal,no-journal,compact,inodes,tmpfs,prezeroed,tmpfs-prezeroed > alpine.jsonl
```

Repeat with `mirror.gcr.io/library/ubuntu:24.04` and
`gcr.io/flame-public/rbe-ubuntu24-04:latest`. Fetch uses the repository's
`mutate.Extract` pipeline, with linux/amd64 selected explicitly. Fetch/network
transfer and writing the flattened input tar are **outside** the measured
conversion. Therefore unpack reads include reading a local uncompressed tar,
where production receives an extracted stream. `read_bytes` is actual storage
IO, not bytes consumed from cached files or network sockets.

Each JSON line includes `/proc/self/io` deltas for unpack, estimate, mke2fs,
hardlink insertion, unpack cleanup, conversion flush, sparse chunk copying and
chunk flush. Linux includes waited-for children's IO in this proc file; the
tool waits for each child before reading its counters. `Conversion` ends after
unpack deletion and flushing the completed image. `Total` additionally includes
chunk creation and its flush. `write_bytes`, `cancelled_write_bytes`, and their
difference are reported separately. These process counters exclude filesystem
journal/metadata IO performed on behalf of kernel threads, and do not measure
registry downloading or other processes.

`Apparent` is `stat.Size()` (`du --apparent-size -B1`); `Allocated` is
`stat.Blocks*512` (`du -B1`). CacheAllocated measures a real hardlink, matching
the default Linux fastcopy path. Chunk copying mirrors the production
`ConvertFileToCOW` SEEK_DATA/SEEK_HOLE loop in 4 MiB chunks. It isolates extent
copying, omitting application cache bookkeeping, hashing and remote upload.
It also reports aggregate allocated bytes of the chunks. This is **not** a
benchmark of the complete snaploader or filecache services. Production
filecache/conversion integration tests cover the changed flags separately.

Every output is checked with `e2fsck -fn`, mounted with `mount -o loop,ro`, and
compared with baseline using a canonical tree manifest: SHA-256 of every
regular file, type and mode, UID/GID, symlink target, hardlink group, device
major/minor, all xattrs (including symlink xattrs), and nanosecond mtime. It
includes root and lost+found. Directory sizes and physical inode numbers are
not tree properties and are omitted. Baseline's tree is saved beside its image.
The baseline image is retained; pass `-retain` to keep every image. Outputs,
mountpoint, and temporary dirs must be dedicated to this tool.

GNU tar can revisit directories whose mtimes it has already restored, changing
them to extraction time. To make repetitions comparable, **only this benchmark**
restores directory mtimes from the archive after extraction, giving implicit
directories a fixed time. Files and symlinks retain their original timestamps.
`E2FSPROGS_FAKE_TIME=1700000000` fixes the time of mkfs-created root/lost+found;
it does not clamp input-file mtimes. Production behavior is unchanged.

Configurations:

| Name | Change from baseline |
| --- | --- |
| baseline | Current ext4 arguments and 1.2 × estimate + 4 MiB size |
| lazy | `-E lazy_itable_init=1,lazy_journal_init=1` |
| nodiscard | `-E nodiscard` |
| lazy-nodiscard | Both of the above |
| small-journal | lazy-nodiscard plus `-J size=4` |
| no-journal | lazy inode init, nodiscard, `-O ^has_journal,^64bit` |
| compact | lazy-nodiscard, estimate + 4 MiB size |
| inodes | compact, `-N entries + max(entries/10,128)` |
| tmpfs | Baseline mkfs, private tmpfs unpack mount |
| prezeroed | `-E assume_storage_prezeroed=1,nodiscard` |
| tmpfs-prezeroed | Both of the above |
| no-fsync | Baseline with benchmark-only fsync interposer |
| tar-no-same | Baseline plus `--no-same-owner --no-same-permissions` (can change the tree) |

Tmpfs mounts are capped by `-tmpfs-bytes` (default 2 GiB). Unlike production,
this benchmark fails rather than silently falling back when a mount/extraction
fails, so reported tmpfs runs actually use tmpfs.

For the **benchmark-only** fsync experiment:

```sh
cc -shared -fPIC -o /var/tmp/ext4-bench/alpine/no_fsync.so \
  enterprise/tools/ext4_bench/interposer/no_fsync.c
sudo "$BENCH" -tar /var/tmp/ext4-bench/alpine.tar -image alpine \
  -work /var/tmp/ext4-bench/alpine -configs no-fsync -runs 3
```

The tool loads this library only into mke2fs, then deletes the unpack tree and
runs `sync` before reporting conversion totals. Never load it into the executor.
Suppressing fsync without a durability replacement changes crash behavior.
Use `-verify=false` to measure intentionally incompatible tar flags; compare
retained images afterwards, so a mismatch does not abort the run sequence:

```sh
sudo "$BENCH" -compare /path/baseline.ext4,/path/candidate.ext4 -work /var/tmp/check
```

Additional probes (use a fresh compatibility work directory):

```sh
sudo python3 enterprise/tools/ext4_bench/empty.py /var/tmp/ext4-bench/empty > empty.jsonl
sudo python3 enterprise/tools/ext4_bench/sizing.py /var/tmp/ext4-bench \
  enterprise/tools/ext4_bench/measurements > sizing.jsonl
sudo python3 enterprise/tools/ext4_bench/check_layout.py /var/tmp/ext4-bench/compat-new \
  "$BENCH" > compatibility.log 2>&1
python3 enterprise/tools/ext4_bench/summarize.py
```

The compatibility fixture covers disk and tmpfs sources, xattrs/capabilities,
devices/FIFO, ownership/modes, hardlinks/symlinks and nanosecond mtimes, then
checks writable mounts and online growth with the same ioctl used by goinit.
Sizing probes use retained baseline images and report success thresholds at
1 MiB resolution; they are separate from timed benchmarks.
