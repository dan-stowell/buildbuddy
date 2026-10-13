# Task: audit end-to-end conversion and caching for redundant disk IO

## Goal
Look past the tar to mke2fs core and find redundant reads and writes in the whole path that leads to and follows an ext4 conversion, then fix the cheap wins. Trace and measure:
- **Layer fetch**: when `resolver.Resolve` is used with and without `useOCIFetcher` (`enterprise/server/util/oci`, `ocifetcher`, any layer or blob caches), are compressed layers written to disk and read back before extraction? Are they cached on disk as well as being converted (double storage)? Is anything decompressed to disk?
- **Filecache add**: does `AddFile` rename, hardlink, reflink, or copy the temp image? The temp image is created under `cacheRoot` while the filecache may sit on a different directory or filesystem. Can the temp image be created where a rename is guaranteed? What does `FastLinkFile` do on use: hardlink, or `fastcopy`/clone?
- **Legacy path**: `hashFile` reads the entire image again. Is that needed?
- **After conversion**: when a VM starts, is the image copied or chunked again (for example into `copy_on_write` chunks or a snapshot)? Is there a way to produce the image directly in the form that is consumed, or to skip a copy?
- **Concurrency and dedupe**: check whether the singleflight key (it includes creds) can cause duplicate conversions of the same image across different credentials, and whether `IsImageCached` races allow double work.
Quantify each finding (bytes and ops per conversion for a typical image), fix the clear wins that are low risk, and list larger ideas with estimated savings in `result.md`. This task overlaps with sibling tasks that rework the tar to mke2fs step itself. Do not rewrite that step; focus on everything around it.

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
