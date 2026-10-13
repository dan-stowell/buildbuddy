1. **Task completion: partial.** The 13-shard split preserves the runfiles scenarios and separates the named tests. Saved XML confirms every case passed 5/5 before and after; median longest-shard time fell from 92.251s to 31.710s. However, the required polling cap and elapsed-time deadline were deliberately omitted.

2. **Required fix:** [remote_bazel_test.go:88](/home/exedev/repo/enterprise/server/test/integration/remote_bazel/remote_bazel_test.go:88), also lines 110 and 130, still doubles delays from 50ms through 51.2s. The loop bounds the delay rather than elapsed time, accumulating approximately 102.35s of sleeps plus RPC time. Implement the requested 100ms cap and elapsed-time deadline while preserving the overall timeout. No correctness regression found in the scenario extraction or sharding.

3. **Missing tests:** Verify capped polling when readiness is delayed and deadline termination when readiness never arrives. Repeat `TestCancel` after implementing the polling change. Existing five-run evidence adequately covers the scenario split; I inspected saved XML rather than rerunning Bazel.

VERDICT: changes-needed
