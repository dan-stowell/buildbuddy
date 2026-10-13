1. **Task accomplished:** 13 shards separate the requested tests and the three runfiles scenarios. Coverage and assertions are preserved. Polling caps at 100 ms with an elapsed deadline retaining the old 102.35-second sleep budget. Saved remote XML confirms every case passed **5/5**, with median longest-shard time decreasing **92.251 → 22.524 seconds**.

2. **Correctness bugs:** None found in the branch diff. No uncommitted tracked changes were present.

3. **Missing tests:** No focused tests exercise polling deadline expiry, stalled RPCs, or the interval cap. This is a nonblocking gap; repeated integration runs cover successful polling.

I independently checked the saved XML, but did not rerun Bazel. No files were modified.

VERDICT: approve