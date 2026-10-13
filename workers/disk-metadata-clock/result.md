# Disk cache timestamp readiness

Changed only `server/backends/disk_cache/disk_cache_test.go`.

## Premise and implementation

This checkout had seven 1-second timestamp sleeps (7 seconds), plus one 500ms restart janitor sleep, rather than the survey's 22.2 seconds. Remote baseline confirmed the saving is real: TestMetadata took 7.09 seconds.

Replaced the timestamp sleeps with a bounded, synchronous readiness loop. A one-byte probe uses the same cache Set/Metadata operations and filesystem, in a separate instance namespace to avoid touching test data. It retries until both the observed atime and mtime, at metadata's microsecond precision, strictly exceed the original reference values. The 1ms interval is only a polling interval; readiness depends on observed timestamps, with a 5-second timeout. The probe is deleted afterwards. All original assertions, seven data sizes, and scenarios remain intact, including both strict Greater comparisons and the unchanged timestamps from repeated Metadata calls.

Replaced the restart GC sleep with WaitUntilMapped followed by the existing bounded waitForEviction helper. Mapping must finish first because an empty, unmapped LRU can misleadingly report that it is already within its target size. All oldest-file assertions remain.

## Validation and timings

All testing used BuildBuddy Remote Bazel; no local fallback was needed. Baseline command:

```sh
bb remote test //server/backends/disk_cache:disk_cache_test --test_arg=-test.v --test_output=all --nocache_test_results
```

Final command used the same flags plus `--runs_per_test=5`. Timing method in both cases: Go's verbose `--- PASS` durations, excluding Bazel analysis, compilation, runner queuing, and upload. Baseline is one run; final is the mean of five independent Bazel test executions (which can contend with one another). Durations are rounded by Go to hundredths of a second.

| Case | Before | After mean (range) | Saving |
| --- | --- | --- | --- |
| TestMetadata | 7.09s | 0.186s (0.13–0.21s) | 6.904s |
| TestJanitorThread | 0.68s | 0.424s (0.37–0.55s) | 0.256s |
| Sum of all 22 case durations | 9.63s | 4.032s | 5.598s |

The timestamp case alone saved approximately 6.9 seconds. Suite sums are an aggregate of case durations, not end-to-end command latency. Parallel repeated executions introduce contention in other tests, so the timestamp case is the clearest measurement of this change.

Baseline: 22/22 cases passed. Final code: 5/5 complete target executions passed; 110/110 case executions passed, zero failures. Two intermediate versions also each passed 5/5 target executions, but the final-code count above is the relevant result. These runs do not establish behavior on every filesystem; readiness observes actual filesystem timestamps instead of assuming 20ms or 1ms is sufficient.

Baseline invocation: https://app.buildbuddy.io/invocation/3549ed93-812a-469d-92d3-de0aa20903b2

Final invocation: https://app.buildbuddy.io/invocation/8ce6e207-379c-4ba6-810f-27f134794eb1

Raw verbose outputs: `/tmp/disk-cache-before-verbose.log` and `/tmp/disk-cache-after-verified.log`.

## Before / after test-case lists

Extracted from the verbose Bazel test output (`=== RUN` and `--- PASS`). There are no nested t.Run cases in this file. Exact same 22-case set before and after:

| Before case | After case | Before passes | Final passes |
| --- | --- | --- | --- |
| TestGetSet | TestGetSet | 1/1 | 5/5 |
| TestMetadata | TestMetadata | 1/1 | 5/5 |
| TestMetadataFileDoesNotExist | TestMetadataFileDoesNotExist | 1/1 | 5/5 |
| TestFindMissing | TestFindMissing | 1/1 | 5/5 |
| TestMultiGetSet | TestMultiGetSet | 1/1 | 5/5 |
| TestReadWrite | TestReadWrite | 1/1 | 5/5 |
| TestReadOffset | TestReadOffset | 1/1 | 5/5 |
| TestReadOffsetLimit | TestReadOffsetLimit | 1/1 | 5/5 |
| TestSizeLimit | TestSizeLimit | 1/1 | 5/5 |
| TestLRU | TestLRU | 1/1 | 5/5 |
| TestFileAtomicity | TestFileAtomicity | 1/1 | 5/5 |
| TestAsyncLoading | TestAsyncLoading | 1/1 | 5/5 |
| TestEviction_V1Layout | TestEviction_V1Layout | 1/1 | 5/5 |
| TestEviction_V1Layout_RootDirEndsInSlash | TestEviction_V1Layout_RootDirEndsInSlash | 1/1 | 5/5 |
| TestJanitorThread | TestJanitorThread | 1/1 | 5/5 |
| TestDefaultToV2Layout | TestDefaultToV2Layout | 1/1 | 5/5 |
| TestZeroLengthFiles | TestZeroLengthFiles | 1/1 | 5/5 |
| TestDeleteStaleTempFiles | TestDeleteStaleTempFiles | 1/1 | 5/5 |
| TestNonDefaultPartition | TestNonDefaultPartition | 1/1 | 5/5 |
| TestV2Layout | TestV2Layout | 1/1 | 5/5 |
| TestV2LayoutMigration | TestV2LayoutMigration | 1/1 | 5/5 |
| TestNewDiskCache_InvalidPartitionStopsStartedPartitions | TestNewDiskCache_InvalidPartitionStopsStartedPartitions | 1/1 | 5/5 |

## Scope and omissions

No tests, assertions, scenarios, or production code were removed or weakened. No dependency, build, or CI files changed. Existing unrelated eviction polling remains unchanged. No fixed 20ms substitute was used. No push or PR was performed. `result.md` is intentionally not committed. The origin fetch URL is restored to the internal mirror after testing.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3