# Task: tune mke2fs and image layout to cut ext4 conversion IO

## Goal
Keep the current unpack-dir pipeline, but minimize the IO done by `mke2fs` and by the image file itself. Investigate and measure:
- **Zero writes and sparseness**: does mke2fs write zeros for inode tables, the journal, or unused space? Try `-E lazy_itable_init=1,lazy_journal_init=1,nodiscard`, check that the output stays sparse (allocated vs apparent size), and check whether any later step (filecache add, `fastcopy`, snapshot or chunking code in `snaploader`/`copy_on_write`) de-sparsifies it.
- **Journal**: is the containerfs mounted read-only in the guest? Search `firecracker.go`, `vmexec`, `goinit`, and the guest mount code. If it is, `-O ^has_journal` (or a minimal `-J size=`) saves the journal writes. Report how the image is actually used before recommending this.
- **Sizing**: `DirectoryToImageAutoSize` uses 1.2x the estimate plus 4MB. Measure how much slack is really needed, and whether a smaller image (fewer inode tables and bitmaps) reduces writes. Also check `-N`/`-i` inode count choices.
- **Page cache and fsync behavior**: is the unpack dir written and then deleted before writeback? Check whether `tar` or `mke2fs` fsyncs, and whether options such as `mke2fs` without a final fsync, or tar `--no-same-*` flags, change the bytes that actually reach disk (`write_bytes - cancelled_write_bytes`).
- **Putting the unpack dir on tmpfs**: where available, and under a size threshold.
Some of these may also apply to `MakeEmptyImage` (workspace and scratch disks). Note any findings there.

## Context: the current pipeline
`enterprise/server/oci/ociconv/ociconv.go` `ConvertContainerToExt4FS` converts an OCI image to ext4 for Firecracker VMs:
1. `mutate.Extract(img)` streams the flattened rootfs tar (layers fetched from the registry);
2. `tar --extract` writes it into a temp dir `unpack-*` under the cache root (full rootfs written to disk);
3. `ext4.DirectoryToImageAutoSize` (`enterprise/server/util/ext4/ext4.go`) walks the dir to estimate a size, then runs `mke2fs -d <dir>`, which reads the whole tree back and writes the image;
4. `createExt4Image` adds the image to the filecache (`AddFile`, `enterprise/server/remote_execution/filecache/filecache.go`), or hashes and renames it in legacy mode;
5. the unpack dir is deleted.
So each conversion writes at least 2x the rootfs size and reads at least 1x. The overall goal is to **reduce disk IO (bytes and ops written and read) per conversion** without changing what the guest sees.

## Measuring (required)
Build a small, repeatable benchmark: a Go test or `go run` tool (it can live under `enterprise/server/oci/ociconv/` or `enterprise/tools/`) that converts real images and reports, per phase and in total:
- bytes written and read (from `/proc/<pid>/io` `write_bytes`/`read_bytes` for the Go process and its children; or put the run in a cgroup and read `io.stat`; `rusage.Oublock` is acceptable as a cross-check);
- wall time;
- final image size (apparent size and allocated blocks from `du`).
Drop the page cache between runs (`sync; echo 3 | sudo tee /proc/sys/vm/drop_caches`) and run each config at least 3 times. Use at least these images: `mirror.gcr.io/library/alpine:3.20`, `mirror.gcr.io/library/ubuntu:24.04`, and one large one such as `gcr.io/flame-public/rbe-ubuntu24-04:latest` (or another public image of 1GB or more if that one isn't available). Note that `write_bytes` counts dirty page-cache bytes that are later truncated away, so also report `cancelled_write_bytes`.

Check correctness: the new image must mount (`sudo mount -o loop,ro`), and its tree must match the baseline image's tree, including file contents, modes, uid/gid, symlinks, hardlinks, device nodes, xattrs, and mtimes. `diff -r` plus a `find -printf` listing diff is fine. Also run `e2fsck -fn` on it.

You are root-capable on this VM (`sudo`). Builds and tests use `bazel` (BuildBuddy `bb`), e.g. `bazel test //enterprise/server/util/ext4/...`; plain `go test` is fine for quick iteration.

## Deliverables
- Commit your changes on the current branch. Keep new behavior **behind a flag that defaults to off** (follow existing `flag.Bool(..., flag.Internal)` patterns) unless it is a strictly safe improvement.
- Write `result.md` covering: the approaches you tried, a results table (baseline vs each variant: bytes written, bytes read, wall time, image size, per image), correctness checks done, risks and compatibility concerns, and a recommendation. Negative results are valuable; record them.
- Existing tests in the touched packages must pass, and add tests for new code paths.
