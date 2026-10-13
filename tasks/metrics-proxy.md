# Task: add production metrics to decide which cache proxy ↔ app IO optimizations are worth shipping

## Goal
We have candidate optimizations but no production data on how often each situation happens. Add Prometheus metrics so we can measure that **before** changing behavior. First, inventory what `server/metrics/metrics.go` and the relevant code already export, and reuse existing metrics where they answer the question. Then add what's missing.

Code: `enterprise/server/cmd/cache_proxy`, `enterprise/server/{byte_stream,content_addressable_storage,action_cache,capabilities,ocifetcher}_server_proxy`, and `enterprise/server/util/proxy_util`. Questions to answer:
- Remote RPCs and bytes per method, split by whether the request was a local hit or miss.
- ByteStream reads over the chunking threshold where the remote `SplitBlob` returned NotFound and the proxy then served locally (a wasted remote call).
- Partial (offset/limit) reads that miss locally, and how many of those local writes fail to commit.
- Action cache: local hits that still went remote, and how often the remote said 'unchanged' versus returned a different result. For different results, record the age of the local entry in a histogram (time since it was cached), so we can pick a safe TTL.
- FindMissingBlobs: the fraction of requested digests that were also requested within the last 1, 5, and 15 minutes. Use a bounded recently-seen tracker behind a flag. This estimates the hit rate of the existing FindMissing cache before enabling it.
- Compression: bytes on the proxy↔app link by compressor (identity/zstd) and direction. Behind a flag, sample the zstd compressibility ratio of identity-encoded bytes (e.g. 1 in N blobs, capped).
- Hit-tracker RPC bytes sent.
Coordinate with sibling tasks `proxy-splitblob` and `proxy-partial-read`, which will change behavior in the same files. Keep metric hooks small and separate from logic changes.

## Rules
- **No behavior change.** These are observe-only.
- Keep label cardinality low: no digests, image names, group IDs, or invocation IDs as labels. Bucketed sizes, method names, and hit/miss labels are fine.
- If a metric needs extra work on the request path (an extra local lookup, or tracking recently seen keys), put that work behind a default-off flag, bound its memory (e.g. an LRU with a fixed size), and say so in the flag help text.
- Follow existing naming and help-text conventions in `server/metrics/metrics.go`. Add tests for any non-trivial logic, such as the recently-seen trackers.
- Run `bazel test --nocache_test_results` on the touched packages.
- `result.md` is the PR description. Include a table with one row per metric: the question it answers, its type and labels, whether it is new or existing, and an example PromQL query that answers the question.
