This standalone Go module benchmarks real OCI images using the same baseline
mke2fs arguments and the executor's tar normalization and sizing code. It keeps
the experimental MIT-licensed hcsshim dependency out of the executor's Go module.
See [result.md](../../../result.md) for the measured results and compatibility limits.

Build and run from this directory (Go version matches the repository):

```sh
go build -o /tmp/oci-ext4-benchmark .
sudo mkdir -p /tmp/oci-ext4-tmpfs
sudo mount -t tmpfs -o size=128M tmpfs /tmp/oci-ext4-tmpfs
sudo /tmp/oci-ext4-benchmark \
  --image mirror.gcr.io/library/alpine:3.20 \
  --work /tmp/oci-ext4-alpine \
  --variants baseline,mke2fs-strict,mke2fs,mke2fs-raw,hcsshim,hcsshim-normalized,tmpfs \
  --tmpfs-root /tmp/oci-ext4-tmpfs --runs 3 > /tmp/alpine.jsonl
sudo ./validate.sh /tmp/oci-ext4-alpine /tmp/alpine-checks
python3 ./summarize.py /tmp/alpine.jsonl
sudo umount /tmp/oci-ext4-tmpfs
```

Use a separate `--work` directory for each image. Repeat for
`mirror.gcr.io/library/ubuntu:24.04` and
`gcr.io/flame-public/rbe-ubuntu24-04:latest`; omit `tmpfs` when the rootfs exceeds
its memory budget. The root-capable runner drops caches before **every** run with
`sync` followed by writing `3` to `/proc/sys/vm/drop_caches`; it fails if this is
not permitted. No sync occurs between measured phases. Existing output images
are removed before each run; the last image for each variant is retained.

Requirements: GNU tar, mke2fs >=1.47.1 with libarchive support (experiments used
1.47.2), e2fsck, loop mounting, Python 3, and `du`. Public registry images are
fetched once into an OCI layout, then reopened locally. Registry fetch I/O and
wall time are reported separately on the first invocation. Timed runs begin
with cold local compressed layers, removing network throughput as a variable.
The benchmark prints the immutable manifest digest and flattened regular-file
payload size. This models conversion of already fetched layers, not cold
end-to-end registry resolution or production filecache lookup performance.

Variants:

- `baseline`: flattened tar -> GNU tar extraction on disk -> directory walk ->
  mke2fs -> remove unpack tree.
- `mke2fs-strict`: executor path, including its conservative directory-order
  compatibility guard; rejects archives whose directory subtrees are revisited.
- `mke2fs`: unrestricted research prototype, with two tar passes, implicit
  parents, deferred hardlinks, baseline xattr behavior, and final directory
  metadata. **Can change directory mtimes on layered images.**
- `mke2fs-raw`: two passes, but forwards the original flattened tar directly to
  `mke2fs -d -`. **Can retain capability xattrs omitted by the baseline.**
- `hcsshim`: one-pass `tar2ext4.ConvertTarToExt4`, with default writer options.
- `hcsshim-normalized`: the same writer fed normalized tar; strips xattrs and
  handles forward hardlinks but still has writer compatibility differences.
- `tmpfs`: the baseline with unpacking on an explicitly mounted, limited tmpfs.
  This retains extraction and directory read-back, but removes their disk I/O
  when there is no swap. Only use a tmpfs the operator has budgeted.

Default finalization measures a hardlink into a cache path and its removal,
matching the default shared filecache data operation. It does not instantiate
production filecache/LRU machinery or model `local_cache_always_clone`.
`--finalize legacy` additionally hashes the image; `none` omits finalization.

Per-phase and total counters come from `/proc/self/io`, including waited child
processes on Linux. `rusage` blocks (self + waited children) are cross-checks;
these blocks are 512 bytes. Both `write_bytes` and `cancelled_write_bytes` are
reported. `syscr`/`syscw` count read/write system calls, including pipe I/O, not
physical device requests. Total wall time includes cleanup and phase accounting,
but excludes cache drops and the final sync used to stabilize allocated blocks.
`Apparent` is `st_size`; `Allocated` is `st_blocks*512`. Validation also records
`du -B1` and `du -B1 --apparent-size` as independent confirmations.

The checker mounts images read-only and compares content hashes, modes, uid/gid,
symlink targets, hardlink equivalence classes, device major/minor numbers, xattrs,
and nanosecond mtimes. It records but exempts the generated mtimes on `/` and
`lost+found`; these differ even across two baseline conversions. All other mtime
differences are failures. It never reads device nodes or follows symlinks.
`find -printf` listings and `e2fsck -fn` results are retained alongside comparison
JSON. Nonzero filesystem-check or comparison status is recorded, not hidden.

Additional fixture (requires `attr` and `libcap2-bin`):

```sh
sudo ./fixture.sh /tmp/oci-ext4-fixture /tmp/oci-ext4-fixture.tar
sudo /tmp/oci-ext4-benchmark --tar /tmp/oci-ext4-fixture.tar \
  --work /tmp/oci-ext4-fixture-results \
  --variants baseline,mke2fs-strict,mke2fs,mke2fs-raw,hcsshim,hcsshim-normalized \
  --runs 3 > /tmp/fixture.jsonl
sudo ./validate.sh /tmp/oci-ext4-fixture-results /tmp/fixture-checks
```

The fixture includes long names, hardlinks, character/block devices, a FIFO,
suid/sgid permissions, non-root uid/gid, capability and user xattrs, fractional
mtimes, and a sparse 32 MiB file. The benchmark flattens it as an OCI layer,
expanding sparse tar payloads just as `mutate.Extract` does for real images.
