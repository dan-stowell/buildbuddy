# Task: cache proxy — skip needless remote SplitBlob calls for large blobs

## Background (claimed, verify)
The BuildBuddy cache proxy (`enterprise/server/cmd/cache_proxy`) serves ByteStream reads through `enterprise/server/byte_stream_server_proxy/byte_stream_server_proxy.go`.
1. **SplitBlob before the local read.** For blobs over `chunking.MaxChunkSizeBytes` (about 4MB), `ShouldReadChunkedOnProxy` (`server/remote_cache/chunking/chunking.go`, on via `cache_proxy.attempt_chunked_reads=true`) sends `Read` into a chunked path (around line 228). That path calls the remote's `SplitBlob` in `chunkDigests` (around 424-447) *before* checking whether the full blob is already in the local cache (the local read is around 265). The local-manifest shortcut is behind experiment `cache_proxy.cdc_read_fast_path` (default false). For blobs that were never chunked, the remote returns NotFound and the proxy falls back to the local read, so every local hit of a large blob costs a wasted remote RPC plus app-side lookups (`content_addressable_storage_server.go` around 1392-1417).
2. **Manifest not stored on write.** `writeChunked` (around 1249-1470) writes chunks locally and calls remote `SpliceBlob`, but never calls `storeLocalChunkedManifest` (which only runs on the read path, around 441-443). So reads of blobs that were uploaded through the proxy also hit remote `SplitBlob`.

## Goal
Verify both. Then fix them:
- (a) When the full blob is present locally, serve it without contacting the remote. Also consider a short in-memory negative cache for "remote says not chunked".
- (b) Store the chunked manifest locally after a successful `SpliceBlob`, so warm chunked reads need no remote metadata RPC.
Watch for auth: make sure skipping the remote call does not skip any authorization check that the remote call was providing. Check how other local-hit paths in the proxy handle auth.

## Rules
- **Verify first.** These findings come from an unverified code read. Before changing anything, confirm the behavior with a test that fails, or a test that counts remote RPCs or disk writes using a fake/instrumented remote. If the finding is wrong, don't build a fix: write up what really happens in result.md and stop.
- Put the fix behind a flag that defaults to off (`flag.Bool(..., flag.Internal)` or an existing experiment pattern), unless it is strictly safe with no behavior change visible to clients.
- Tests: the new tests show the remote call or disk write is gone with the flag on and unchanged with it off. Existing tests in the touched packages pass; run `bazel test --nocache_test_results` on them.
- Commit, and write `result.md` as a PR description: what you verified (with evidence), the change, tests, risks, and the expected saving per request.
