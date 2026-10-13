# Task: cache proxy — stop wasted local writes on partial (offset/limit) read misses

## Background (claimed, verify)
In `enterprise/server/byte_stream_server_proxy/byte_stream_server_proxy.go`, `readRemoteWriteLocal` (around 780-846) forwards a ByteStream `Read` that has `read_offset`/`read_limit` to the remote unchanged. It then streams the partial data into a local write of the *full* blob starting at offset 0 (`readThroughCacheStream`, around 751-770). The local commit's size/digest check then fails, so the disk writes are wasted, nothing is cached, and every repeat partial read goes remote again.

## Goal
Verify this with a test that counts local bytes written and remote reads for repeated partial reads. Then fix it. When offset or limit is set on a local miss, either:
- skip the local write entirely, or
- (preferred if simple) for blobs under a size threshold, fetch the full blob from the remote, write it locally, and serve the requested range from it, so later partial reads hit locally.
Pick one, justify it in result.md, and handle compressed reads (zstd resource names) correctly.

## Rules
- **Verify first.** These findings come from an unverified code read. Before changing anything, confirm the behavior with a test that fails, or a test that counts remote RPCs or disk writes using a fake/instrumented remote. If the finding is wrong, don't build a fix: write up what really happens in result.md and stop.
- Put the fix behind a flag that defaults to off (`flag.Bool(..., flag.Internal)` or an existing experiment pattern), unless it is strictly safe with no behavior change visible to clients.
- Tests: the new tests show the remote call or disk write is gone with the flag on and unchanged with it off. Existing tests in the touched packages pass; run `bazel test --nocache_test_results` on them.
- Commit, and write `result.md` as a PR description: what you verified (with evidence), the change, tests, risks, and the expected saving per request.
