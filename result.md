# Avoid remote SplitBlob on warm cache proxy reads

Large blobs already cached in full currently incur a remote metadata RPC before
the proxy reads them locally. Successful chunked uploads also leave no local
manifest, forcing subsequent reads to fetch metadata remotely. This change lets
those reads stay local under the default-off experiment
`cache_proxy.local_read_fast_path`.

## Verification before implementation

Both findings were confirmed with unchanged production code:

- `TestReadChunkedFallsBackToLocalBlob` used a gRPC interceptor to count exactly
  one remote `SplitBlob` for a 5 MiB full blob stored only in the proxy cache.
  The remote had no manifest, and the read still returned the local bytes.
- A new assertion in `TestWriteChunked` confirmed `chunking.LoadManifest` returned
  NotFound immediately after a successful 10 MiB chunked proxy upload and remote
  `SpliceBlob`. The existing upload and download assertions passed.

Command: `bazel test --nocache_test_results //enterprise/server/byte_stream_server_proxy:byte_stream_server_proxy_test --test_filter='Test(ReadChunkedFallsBackToLocalBlob|WriteChunked)$'`.
[Passing verification run](https://app.buildbuddy.io/invocation/bbfb1fe8-9f19-4bf3-a6c8-ec974f394def).

The code review also confirmed that remote `SplitBlob` loads a manifest and,
when present, checks chunk availability with `FindMissingBlobs`. NotFound can
mean a missing manifest or missing chunks; it does not necessarily mean the
blob was never chunked.

## Change

When the experiment is enabled, chunk-eligible reads first use the existing local
ByteStream read path. Successful reads return immediately. A failure after
sending any frame returns that error without fallback; a failure before sending
data proceeds to the chunked/remote paths. The local read is attempted only once.

After successful chunk uploads and remote `SpliceBlob`, the proxy stores the
manifest locally on a best-effort basis. The experiment also enables the existing
local-manifest shortcut, including its chunk availability check, so warm reads
need neither `SplitBlob` nor another manifest write. Existing
`cache_proxy.cdc_read_fast_path` behavior remains available independently.
Remote duration metrics exclude the added local manifest write.

All new behavior defaults off, including when no experiment provider is present.
Encrypted requests on proxies without encryption support continue to bypass
local reads and manifest storage.

## Authorization

The early full-blob read calls the same `ReadCASResource` used by existing local
hits, which authenticates through `prefix.AttachUserPrefixToContext` and applies
the caller's group namespace. Remote `SplitBlob` uses the same prefix/auth check;
it provides no additional read capability check in its handler. Local chunked
reads likewise authenticate and attach the prefix before loading manifests.
The existing anonymous-blackhole handling and encryption bypass are preserved.
This relies on the proxy's authenticator and group isolation, as other proxy
local-hit paths already do; it does not add remote credential revalidation.

## Tests

The extended tests compare experiment off/on:

- Full local blobs: one/zero `SplitBlob` calls per eligible read; exact bytes are
  checked for full, offset, limited, and compressed reads. Limited reads already
  avoid `SplitBlob` in both cases.
- Chunked uploads: absent/present local manifest immediately after successful
  splice; zero/one successful manifest stores during upload.
- First compressed and identity post-upload reads: two/zero total `SplitBlob`
  calls and two/zero manifest stores during reads, with exact reconstructed bytes.
- Auth isolation: the owner can read; another group and invalid credentials
  receive no data. Partial local failures do not fall back after sending bytes.
- Existing encrypted remote-only read, fallback, and write tests run with the new
  experiment enabled, retaining their remote-only assertions.

Full suites passed with uncached test results:

```sh
bazel test --nocache_test_results \
  //enterprise/server/byte_stream_server_proxy:byte_stream_server_proxy_test \
  //server/remote_cache/byte_stream_server:byte_stream_server_test \
  //server/remote_cache/chunking:chunking_test
```

[Passing full-suite run](https://app.buildbuddy.io/invocation/de961a50-5b11-438c-85ee-3ddd3f471979).
The full proxy suite also passed after the final remote-duration metric adjustment:
[final proxy run](https://app.buildbuddy.io/invocation/dc209f83-c8e7-4540-8513-3372f9ef1b7e).

## Expected savings and risks

Each eligible full local hit saves one remote `SplitBlob` RPC (one network
round trip and the remote manifest lookup; chunk checks too if a remote manifest
exists). Each warm chunked read saves one metadata RPC, the remote manifest and
chunk-availability lookups, and the local manifest rewrite. This moves one local
manifest write to successful upload completion. No latency benchmark was run.

Chunked or cold blobs incur an extra full-blob local lookup before the chunked
path when enabled. Manifest storage adds local metadata space and upload work;
store failures remain nonfatal and fall back to remote metadata on later reads.
Eviction or missing chunks prevents the local manifest shortcut from being used.
Full local blobs take precedence over chunk representations and are counted as
ordinary local hits. Remote outages or deletion no longer block valid local hits,
consistent with existing nonchunked local reads.

A negative cache was considered and deferred: NotFound also covers missing chunks,
and caching it could suppress newly published/repaired chunk manifests. Serving
the full local blob first already removes repeated metadata RPCs for the reported
local-hit case without adding cache lifetime, capacity, or invalidation rules.
