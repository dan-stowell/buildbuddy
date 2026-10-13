# Task: add production metrics to decide which Firecracker ext4 image conversion IO optimizations are worth shipping

## Goal
We have candidate optimizations but no production data on how often each situation happens. Add Prometheus metrics so we can measure that **before** changing behavior. First, inventory what `server/metrics/metrics.go` and the relevant code already export, and reuse existing metrics where they answer the question. Then add what's missing.

Code: `enterprise/server/oci/ociconv/ociconv.go`, `enterprise/server/util/ext4/ext4.go`, the filecache (`enterprise/server/remote_execution/filecache`), and the firecracker container's image-pull path. Questions to answer:
- How many conversions per executor, versus ext4-image cache hits at lookup (`IsImageCached`/`LinkCachedImage`)?
- How often is an image converted again after being evicted from the filecache? (Track recently converted cache keys in a bounded set behind a flag.)
- How often does the singleflight end up running concurrent conversions of the same image under different credentials? Also count waiters deduped by singleflight.
- Per conversion: bytes written and read, broken down by phase (extract, size, mke2fs, filecache add, cleanup). Use child rusage and `/proc/self/io` deltas where possible. Also record duration per phase and final image size (apparent and allocated) as histograms.
- How long do tasks wait on image conversion, as part of the pull/init phase?
- The e2fsprogs (`mke2fs -V`) version as an info-style gauge, recorded once at startup.
Keep edits to `ociconv.go` minimal and localized. Open PRs #6/#7/#8 also touch that file, so avoid reshaping functions.

## Rules
- **No behavior change.** These are observe-only.
- Keep label cardinality low: no digests, image names, group IDs, or invocation IDs as labels. Bucketed sizes, method names, and hit/miss labels are fine.
- If a metric needs extra work on the request path (an extra local lookup, or tracking recently seen keys), put that work behind a default-off flag, bound its memory (e.g. an LRU with a fixed size), and say so in the flag help text.
- Follow existing naming and help-text conventions in `server/metrics/metrics.go`. Add tests for any non-trivial logic, such as the recently-seen trackers.
- Run `bazel test --nocache_test_results` on the touched packages.
- `result.md` is the PR description. Include a table with one row per metric: the question it answers, its type and labels, whether it is new or existing, and an example PromQL query that answers the question.
