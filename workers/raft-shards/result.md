# Shard slow raft test targets

Raft integration test targets spend substantial wall time running independent cases sequentially. Set `shard_count = 4` for metadata, client, and sender, and increase store from 8 to 16 shards. Coverage and test behavior are preserved: the only committed changes are the four BUILD attributes, with one commit per package.

Remote validation passed five uncached runs per target with retries disabled. Before/after XML case sets are identical. Store's paired wall time, including executor overhead, improved from 143.719 to 128.116 seconds (10.9%), satisfying the criterion for retaining 16 shards. Client's paired measurement also improved, although its five-run batch showed runner variability. Metadata had a pre-existing baseline failure followed by a passing retry; all five sharded runs passed.

## Method and premise

All tests ran through BuildBuddy Remote Bazel with `--config=ci --nocache_test_results --test_output=errors`. No local test fallback was needed.

Baseline invocation: https://app.buildbuddy.io/invocation/f1968b6d-2d5a-4f1b-949a-763ddc58a80f . Candidate client/sender/store: https://app.buildbuddy.io/invocation/2a2a3395-b596-4574-a856-e0cf5c3a3453 . Candidate metadata: https://app.buildbuddy.io/invocation/e8cbb09e-0567-48f3-b695-bbc46e58b663 .

The baseline confirms 20/4/7 top-level tests and store at 8 shards. The surveyed durations are not exact for this revision/environment. For a consistent before/after measure, the table sums top-level testsuite durations within each XML shard and takes the maximum shard. This measures the test-body critical path, excludes runner provisioning, Bazel analysis, and executor overhead, and does not double-count client subtests. After values are the range and median of five uncached runs.

| Package | Baseline XML critical path (s) | Candidate range (s) | Candidate median (s) |
|---|---:|---:|---:|
| metadata | 113.442 | 44.118–46.203 | 45.347 |
| client | 22.963 | 23.260–23.891 | 23.510 |
| sender | 31.493 | 9.010–9.262 | 9.214 |
| store | 140.617 | 122.416–123.004 | 122.804 |

Metadata baseline was **FLAKY**: the first attempt stopped in `TestDistributedRanges` (XML: "No pass/skip/fail event found for test"; Pebble log reports a missing test directory/log during shutdown). Its retry passed all 20 cases. The table uses the complete passing retry; this is a baseline limitation, not evidence that the pre-existing flake is fixed. Candidate runs disabled retries with `--flaky_test_attempts=1`.

## Paired wall-time measurements including overhead

Additional uncached, retry-disabled runs used `bb remote --script` to run the same target sequentially with `--test_sharding_strategy=forced=N`, writing Bazel JSON build events. Each row uses the same method before and after: maximum `testResult.executionInfo.timingBreakdown.totalTime`, including execution, queue, upload, setup, output processing, parsing, and network overhead. Execution alone includes process startup and teardown. The start-to-finish envelope across shards is also reported to include differing shard start times. These timings exclude repository checkout and Bazel analysis.

| Target | Before shards | After shards | Max execution before → after | Max with overhead before → after | Shard envelope before → after |
|---|---:|---:|---:|---:|---:|
| client | 1 | 4 | 29.907 → 19.238 s | 30.566 → 20.057 s | 30.566 → 20.059 s |
| store | 8 | 16 | 143.320 → 127.026 s | 143.719 → 128.116 s | 143.719 → 128.119 s |

Store therefore meets the keep criterion: overhead-adjusted wall time improves by **15.603 seconds (10.9%)**, and the full shard envelope improves by 15.600 seconds. The change retains 16 shards. Both comparisons passed every shard, in addition to the five-run validation above. Store paired runner: https://app.buildbuddy.io/invocation/cf217343-8a88-460c-b1a2-4af72158967c (Bazel invocations dcab0133-052d-4f96-97ee-982d70e68f0f and ec5093dd-3c6e-440b-8442-36a17f49904f). Client paired runner: https://app.buildbuddy.io/invocation/a4694c62-0ea7-4637-b0a7-82476c890ecd .

Client shows variability: the initial unsharded baseline was 23.197 seconds of action duration, whereas the five-run sharded batch had an XML critical path near 24 seconds. The paired run supports a smaller saving than the modeled 40 seconds, but that benefit is not uniform across runner conditions. Aggregate execution work is not guaranteed unchanged: cold fixture/setup costs are repeated per process. Metadata and sender XML critical-path reductions are substantial; their table does not claim executor-inclusive paired measurements.

What was not done: no test setup sharing, top-level case extraction, sleep/timeout changes, assertion changes, isolation fixes, CI changes, pushes, or PRs. The modeled ~80-second store saving and ~40-second client saving were not reproduced. No claim that the existing metadata baseline flake is cured; five candidate runs are evidence of stability, not a guarantee.

## Repeated runs and coverage

Candidates used `--runs_per_test=5 --flaky_test_attempts=1`. Metadata: 20/20 shard executions, 100/100 case executions. Client: 20/20 shards, 80/80 case executions including subtests. Sender: 20/20 shards, 35/35 cases. Store: 80/80 shards, 195/195 cases. No candidate failures or skips. Every run was independently checked against the baseline XML case-name set, with exact equality and no failure/error/skipped elements.

Baseline client, sender, and all 8 store shards passed on their first attempts. Metadata passed on its second attempt. Lists below are taken from passing baseline and candidate XML, including nested cases. Each table explicitly lists both sets.

## Isolation audit

`flags.Set` saves each original flag and restores it via `t.Cleanup`; no top-level `t.Parallel`, direct `flag.Set`, or persistent cross-case environment edits were found. Sharding creates separate processes rather than concurrent cases sharing flags. Metadata helpers create fresh configs/authenticators/environments; shared user data is fixture input and the shared filestorer does not carry cluster data between tests. GCS helper tests use independent mock stores and clocks even though they reuse a blob name.

Cluster/proposer fixtures allocate per-test temporary roots (`testfs.MakeTempDir`, `t.TempDir`) and independent sessions, replicas, and stores. Network addresses use `testport.FindFree`; the Unix helper also takes cross-process flock locks. Numeric range/replica IDs are scoped to fresh fixture stores. No test relies on a previous test creating files, reserving a fixed port, or setting flags. No isolation source fix was needed.

## Test-case lists (before and after)

### metadata

| Before | After (each of 5 runs; PASS) |
|---|---|
| `TestAutoBringup` | `TestAutoBringup` |
| `TestCacheShutdown` | `TestCacheShutdown` |
| `TestDistributedRanges` | `TestDistributedRanges` |
| `TestFindMissingMetadata` | `TestFindMissingMetadata` |
| `TestFindReportsExpiredGCSObjectAbsent` | `TestFindReportsExpiredGCSObjectAbsent` |
| `TestGCSObjectMayBeExpired_InlineRecord` | `TestGCSObjectMayBeExpired_InlineRecord` |
| `TestGCSObjectMayBeExpired_PastTTL` | `TestGCSObjectMayBeExpired_PastTTL` |
| `TestGCSObjectMayBeExpired_RecentObject` | `TestGCSObjectMayBeExpired_RecentObject` |
| `TestGCSObjectMayBeExpired_TTLDisabled` | `TestGCSObjectMayBeExpired_TTLDisabled` |
| `TestGCSObjectMayBeExpired_WithinBufferOfTTL` | `TestGCSObjectMayBeExpired_WithinBufferOfTTL` |
| `TestGetAndSet` | `TestGetAndSet` |
| `TestLRU` | `TestLRU` |
| `TestMaybeUpdateGCSAtime_FailedRefreshRecordsNoCustomTime` | `TestMaybeUpdateGCSAtime_FailedRefreshRecordsNoCustomTime` |
| `TestMaybeUpdateGCSAtime_FutureCustomTimeSkipsRefresh` | `TestMaybeUpdateGCSAtime_FutureCustomTimeSkipsRefresh` |
| `TestMaybeUpdateGCSAtime_NilMetadataIsNoop` | `TestMaybeUpdateGCSAtime_NilMetadataIsNoop` |
| `TestMaybeUpdateGCSAtime_PastTTLSkipsRefresh` | `TestMaybeUpdateGCSAtime_PastTTLSkipsRefresh` |
| `TestMaybeUpdateGCSAtime_PastThresholdRefreshes` | `TestMaybeUpdateGCSAtime_PastThresholdRefreshes` |
| `TestMaybeUpdateGCSAtime_TTLDisabledSkipsRefresh` | `TestMaybeUpdateGCSAtime_TTLDisabledSkipsRefresh` |
| `TestMaybeUpdateGCSAtime_WithinThresholdSkipsRefresh` | `TestMaybeUpdateGCSAtime_WithinThresholdSkipsRefresh` |
| `TestMaybeUpdateGCSAtime_ZeroThresholdRefreshesEveryTime` | `TestMaybeUpdateGCSAtime_ZeroThresholdRefreshesEveryTime` |

### client

| Before | After (each of 5 runs; PASS) |
|---|---|
| `TestRefreshSession` | `TestRefreshSession` |
| `TestRunNodehostFn_ErrorCodes` | `TestRunNodehostFn_ErrorCodes` |
| `TestRunNodehostFn_ErrorCodes/ErrAborted` | `TestRunNodehostFn_ErrorCodes/ErrAborted` |
| `TestRunNodehostFn_ErrorCodes/ErrCanceled` | `TestRunNodehostFn_ErrorCodes/ErrCanceled` |
| `TestRunNodehostFn_ErrorCodes/ErrClosed` | `TestRunNodehostFn_ErrorCodes/ErrClosed` |
| `TestRunNodehostFn_ErrorCodes/ErrRejected` | `TestRunNodehostFn_ErrorCodes/ErrRejected` |
| `TestRunNodehostFn_ErrorCodes/ErrShardAlreadyExist` | `TestRunNodehostFn_ErrorCodes/ErrShardAlreadyExist` |
| `TestRunNodehostFn_ErrorCodes/ErrShardClosed` | `TestRunNodehostFn_ErrorCodes/ErrShardClosed` |
| `TestRunNodehostFn_ErrorCodes/ErrShardNotFound` | `TestRunNodehostFn_ErrorCodes/ErrShardNotFound` |
| `TestRunNodehostFn_ErrorCodes/ErrShardNotReady` | `TestRunNodehostFn_ErrorCodes/ErrShardNotReady` |
| `TestRunNodehostFn_ErrorCodes/ErrShardNotStopped` | `TestRunNodehostFn_ErrorCodes/ErrShardNotStopped` |
| `TestRunNodehostFn_ErrorCodes/ErrSystemBusy` | `TestRunNodehostFn_ErrorCodes/ErrSystemBusy` |
| `TestRunNodehostFn_ErrorCodes/ErrTimeout` | `TestRunNodehostFn_ErrorCodes/ErrTimeout` |
| `TestRunNodehostFn_ErrorCodes/OutOfRange` | `TestRunNodehostFn_ErrorCodes/OutOfRange` |
| `TestSession` | `TestSession` |
| `TestSessionInParallel` | `TestSessionInParallel` |

### sender

| Before | After (each of 5 runs; PASS) |
|---|---|
| `TestFetchPartitionDescriptors` | `TestFetchPartitionDescriptors` |
| `TestLookupRangeDescriptor` | `TestLookupRangeDescriptor` |
| `TestLookupRangeDescriptorsForPartition` | `TestLookupRangeDescriptorsForPartition` |
| `TestRunMultiKeyReturnsPartialResultsOnError` | `TestRunMultiKeyReturnsPartialResultsOnError` |
| `TestSyncProposeCASIsIdempotentOnReplicaRetry` | `TestSyncProposeCASIsIdempotentOnReplicaRetry` |
| `TestSyncProposeIsIdempotentOnReplicaRetry` | `TestSyncProposeIsIdempotentOnReplicaRetry` |
| `TestSyncProposeWithRangeDescriptorIsIdempotentOnReplicaRetry` | `TestSyncProposeWithRangeDescriptorIsIdempotentOnReplicaRetry` |

### store

| Before | After (each of 5 runs; PASS) |
|---|---|
| `TestAddGetRemoveRange` | `TestAddGetRemoveRange` |
| `TestAddRangeBack` | `TestAddRangeBack` |
| `TestAddReplica` | `TestAddReplica` |
| `TestAddReplica_ExistingStaging` | `TestAddReplica_ExistingStaging` |
| `TestAddReplica_MetaRange` | `TestAddReplica_MetaRange` |
| `TestAddReplica_NonVoterNotStarted` | `TestAddReplica_NonVoterNotStarted` |
| `TestAddReplica_NonVoterStarted` | `TestAddReplica_NonVoterStarted` |
| `TestAddReplica_Voter` | `TestAddReplica_Voter` |
| `TestAutomaticSplitting` | `TestAutomaticSplitting` |
| `TestBringupSetRanges` | `TestBringupSetRanges` |
| `TestCleanupExpiredSessions` | `TestCleanupExpiredSessions` |
| `TestCleanupZombieInitialMembersNotSetUp` | `TestCleanupZombieInitialMembersNotSetUp` |
| `TestCleanupZombieRangeDescriptorNotInMetaRange` | `TestCleanupZombieRangeDescriptorNotInMetaRange` |
| `TestCleanupZombieReplicaNotInRangeDescriptor` | `TestCleanupZombieReplicaNotInRangeDescriptor` |
| `TestConfiguredClusters` | `TestConfiguredClusters` |
| `TestDownReplicate` | `TestDownReplicate` |
| `TestHardDeletePartitions` | `TestHardDeletePartitions` |
| `TestLeasedRange_LeaseInvalid` | `TestLeasedRange_LeaseInvalid` |
| `TestLeasedRange_LeaseNotCurrent` | `TestLeasedRange_LeaseNotCurrent` |
| `TestManySplits` | `TestManySplits` |
| `TestNonSoftDeletedPartitionsNotDeleted` | `TestNonSoftDeletedPartitionsNotDeleted` |
| `TestPostFactoSplit` | `TestPostFactoSplit` |
| `TestRebalance` | `TestRebalance` |
| `TestRemoveData_ShardStartedNoRangeDescriptor` | `TestRemoveData_ShardStartedNoRangeDescriptor` |
| `TestRemoveDeadReplica` | `TestRemoveDeadReplica` |
| `TestRemoveReplicaRemoveData` | `TestRemoveReplicaRemoveData` |
| `TestRemoveReplicaRemoveData_StagingReplicaNotAdded` | `TestRemoveReplicaRemoveData_StagingReplicaNotAdded` |
| `TestRemoveReplicaRemoveData_StagingReplicaStarted` | `TestRemoveReplicaRemoveData_StagingReplicaStarted` |
| `TestReplaceDeadReplica` | `TestReplaceDeadReplica` |
| `TestSetupNewPartitions` | `TestSetupNewPartitions` |
| `TestSoftDeletePartitions` | `TestSoftDeletePartitions` |
| `TestSplitAcrossClusters` | `TestSplitAcrossClusters` |
| `TestSplitMetaRange` | `TestSplitMetaRange` |
| `TestSplitNonMetaRange` | `TestSplitNonMetaRange` |
| `TestStartMissingShard` | `TestStartMissingShard` |
| `TestTxnRollbackMarkerGC` | `TestTxnRollbackMarkerGC` |
| `TestUpReplicate` | `TestUpReplicate` |
| `TestUpdateRangeDescriptor` | `TestUpdateRangeDescriptor` |
| `TestZoneAwareRebalance` | `TestZoneAwareRebalance` |



---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3