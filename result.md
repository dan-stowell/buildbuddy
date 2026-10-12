A local cache miss for a ByteStream offset/limit read currently writes the returned range into an upload of the full blob. Enable `cache_proxy.skip_partial_read_cache_writes` to forward these reads directly to the remote and avoid the discarded local writes. The internal flag defaults to false.

Verification preceded the production change. `TestPartialReadMissLocalWrites` uses real remote and local ByteStream servers, a remote RPC counter, and an instrumented local cache writer. With a 4,096-byte blob and two identical reads, the original implementation returned the correct range but produced these counts:

| Request | Remote reads | Local bytes written | Blob cached |
| --- | ---: | ---: | --- |
| offset=100 | 2 | 7,992 | No |
| limit=200 | 2 | 400 | No |
| offset=100, limit=200 | 2 | 400 | No |
| zstd, offset=100 | 2 | 7,992 | No |
| Full read, identity or zstd | 1 | 4,096 | Yes |

The baseline test passed before the fix. The write handler checks the full resource checksum before committing; incomplete responses fail that check. The test measures bytes accepted by the cache writer (the test environment uses memory cache), not physical disk I/O. This confirms the wasted local cache writes and repeated remote reads without relying on a disk-backend estimate.

The change reuses `readRemoteOnly` when either offset or limit is nonzero. It preserves the original request, resource name, remote response frames, and streaming/error handling. Zstd ranges stay under the remote server's control; the proxy does not slice compressed bytes or reinterpret offsets. Full reads and local hits continue through their existing paths.

I chose the permitted skip-write option. Fetching full blobs would add remote traffic and latency on the first partial miss and require a threshold policy and careful compressed range handling. Skipping the upload is a small streaming change with no extra download or buffering.

Validation passed:

- Before the fix: `bazel test --nocache_test_results //enterprise/server/byte_stream_server_proxy:byte_stream_server_proxy_test --test_filter=TestPartialReadMissLocalWrites --test_output=all --bes_backend= --bes_results_url=`.
- After the fix: the same command passed all 16 flag-off/on cases: offset, limit, combined, their zstd equivalents, and full identity/zstd controls. With the flag off, counts match the baseline. With it on, every partial miss writes zero local bytes; both repeated requests still go remote. Decompressed zstd responses match the requested range. Full reads still cache and serve the second request locally.
- Existing package tests: `bazel test --nocache_test_results //enterprise/server/byte_stream_server_proxy:all --test_output=errors --bes_backend= --bes_results_url=` passed, including both test shards.
- `git diff --check` passed. Build-event uploads were disabled because the upload service failed on the initial run.

Expected saving per incomplete miss: all local payload writes, writer setup/cleanup, and failed full-blob validation. In the measured cases this saves 3,996 bytes per offset request or 200 bytes per limited request, including zstd requests after local decompression. Disk-backed caches avoid the corresponding payload writes; exact physical I/O depends on backend buffering and compression. Remote RPC count and remote download bytes do not decrease.

Risks: repeated partial reads remain remote until another request fills the cache. A nonzero limit that actually includes the entire blob also skips caching when enabled, even though it could have committed successfully. Rollout is opt-in and disabling the flag restores the previous behavior. Existing chunked-read paths are outside this fallback change.
