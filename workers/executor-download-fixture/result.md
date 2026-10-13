# Executor test optimization

## Changes and premise verification

- Reduced the timeout-test input from 128 MiB (134,217,728 bytes) to `dirtools.BatchReadLimitBytes + 1` (2,097,153 bytes today). This removes 132,120,575 bytes (126 MiB minus 1 byte) of input allocation, disk writing, hashing, and logical upload per execution. Network savings were not measured; the original all-zero input is compressible.
- Verified `BatchFileFetcher` selects ByteStream when the digest size is greater than `BatchReadLimitBytes` (currently 2 MiB). Added a delegating ByteStream client that observes the actual `Read` request and asserts its digest size matches the fixture. It preserves the real client and original timeout behavior, error, retry, recycling, completion, and timestamp assertions.
- Replaced the five-minute fake run with a gate on the execution context's cancellation. The ping-failure case additionally waits for the fake run to exit, bounded by a 10-second failure timeout. The original sleep was in a background goroutine: baseline publish failures finished promptly, so there is no five-minute wall-time saving to claim. The gate releases that goroutine promptly and keeps execution pending until the failed ping cancels it.
- Necessary scope exception: added three test-only dependencies to the adjacent `enterprise/server/remote_execution/executor/BUILD` for the cutoff constant, ByteStream proto, and gRPC call-option type. Bazel requires direct dependencies for these imports. No production code, modules, lockfiles, or CI configuration changed.

## Validation and timings

All measurements used **BuildBuddy Remote Bazel**; no local fallback was needed. Baseline was tested before editing, then the original files were temporarily restored for the five-run baseline comparison and the final files restored afterward.

Same command for both repeated measurements:

```sh
bash -lc 'cd ~/repo && bb remote test //enterprise/server/remote_execution/executor:executor_test --test_output=all --nocache_test_results --test_arg=-test.v --runs_per_test=5'
```

Measured individual test durations from the Go verbose output (rounded to hundredths of a second), excluding runner queueing, analysis, compilation, and artifact handling:

| Test | Before seconds (5 runs) | After seconds (5 runs) | Median before → after |
| --- | --- | --- | --- |
| InternalInputDownloadTimeout | 3.43, 3.47, 3.47, 3.48, 3.48 | 0.40, 0.43, 0.42, 0.43, 0.40 | 3.47 → 0.42 s |
| PublishFailures | 0.05, 0.06, 0.06, 0.07, 0.06 | 0.05, 0.06, 0.08, 0.06, 0.08 | 0.06 → 0.06 s |

The repeated input-timeout median improved by 3.05 seconds (88%). Remote execution conditions vary: the initial single-run baseline took 1.05 seconds, so treat the percentage as an observed result, not a guaranteed saving. That initial baseline used the same target and flags without `--runs_per_test=5` and passed every case. End-to-end Bazel elapsed times for the repeated before/after invocations were 65.939/77.231 seconds, respectively; compilation and analysis overhead dominate these and they do not measure test-only savings.

Evidence:

- Initial baseline: https://app.buildbuddy.io/invocation/77120014-3afa-4623-b8f8-b54823e82c13 (local log `/tmp/executor-before.log`).
- Five-run baseline: https://app.buildbuddy.io/invocation/c6b19654-89b5-4a56-945a-bade766810a0 (local log `/tmp/executor-before-repeat.log`).
- Five-run final: https://app.buildbuddy.io/invocation/16e03de8-f6e0-4bcf-a289-e957fee5e555 (local log `/tmp/executor-after.log`).
- `gofmt` applied; `git diff --check` passed.

## Before/after case lists and pass counts

These are the actual case names from Bazel's verbose Go test output, including parent tests and nested cases. Both lists contain exactly the same 23 entries. Every entry passed 5/5 before and 5/5 after (115 reported case passes per version, including parent entries); the initial baseline adds one pass per entry. No cases or existing assertions were deleted, skipped, tagged, or weakened. Five runs provide a flakiness check, not a guarantee.

| Case (both before and after lists) | Before | After |
| --- | --- | --- |
| `TestExecuteTaskAndStreamResults` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults/Auxiliary_metadata` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults/Run_failure` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults/Success` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_CacheHit` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_InternalInputDownloadTimeout` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_MissingInput` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_MissingInput/missing_input_root` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_MissingInput/missing_large_file` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_MissingInput/missing_small_file` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_PostCompletionStats` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_PostCompletionStats/EmptyStats` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_PostCompletionStats/ExperimentOff` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_PostCompletionStats/WithStats` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_PreservesOOMError` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_PreservesOOMErrorAfterGracefulTermination` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_PublishFailures` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_PublishFailures/Ping_failure` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_PublishFailures/Send_failure` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_Timeout` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_Timeout/set_action_timeout` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_Timeout/set_both_action_and_platform_property_timeout` | 5/5 | 5/5 |
| `TestExecuteTaskAndStreamResults_Timeout/set_timeout_platform_property` | 5/5 | 5/5 |

## Choices and completion

Did not change the missing-large-input scenario, unrelated tests, production timeouts, or retry behavior. Did not claim five minutes of runtime savings or measure physical network bytes. No push or PR. `result.md` is left git-ignored and uncommitted. The original mirror fetch URL is restored.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3