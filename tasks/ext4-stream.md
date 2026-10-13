# Task: convert OCI images to ext4 straight from the tar stream (no unpack dir)

## Goal
Remove step 2 (the on-disk unpack directory) and step 3's read-back by building the ext4 image directly from the flattened tar stream. Evaluate these candidates, prototype the most promising one or two, and measure them against the baseline:
- **`mke2fs -d <tarball>`**: e2fsprogs 1.47.1+ can populate from a tar archive via libarchive. Find out whether it can read from a pipe or FIFO (so the tar never hits disk) or needs a seekable file, which e2fsprogs version the executor ships (`dockerfiles/executor_image/Dockerfile`, and the Bazel-provided mke2fs if any), and whether it handles hardlinks, device nodes, xattrs, sparse files, and long names correctly.
- **`github.com/Microsoft/hcsshim/ext4/tar2ext4`** (MIT, pure Go), or another pure-Go ext4 writer: check the feature set it emits (journal, extents, 64bit, inline data, and so on) against what `DirectoryToImage` produces now (`-O ^64bit`, 4K blocks, ext4) and against what the Firecracker guest kernel mounts. Check whether it needs to know the size up front or can compute it from the stream, and how much it buffers in memory or temp files.
- Any other approach you find (for example, unpacking into tmpfs or memory when the image is small enough, under a memory budget).

Also note that `DiskSizeBytes` walks the unpack dir. A streaming approach needs another way to size the image: two passes over the stream, layer metadata, or a growable or sparse output that is truncated at the end.

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
