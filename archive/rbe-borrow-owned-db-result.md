# Borrowed database handles for RBE apps

## Change and ownership

Verified the premise at baseline `770b88bc8`: testenv.GetTestEnv calls setupDBHandle -> GetConfiguredDatabase -> AutoMigrate; AddBuildBuddyServerWithOptions immediately replaces DB/auth/user/invocation handles. Added WithBorrowedDBHandle, which bypasses database creation/migration and never closes the supplied handle. Nil borrowing fails explicitly. WithDBHandle retains its existing ownership/close behavior. Enterprise Options forwards TestEnvOptions. RBE apps borrow the harness DB and retain the existing shared auth/user/invocation wrappers.

Added an ownership regression test: owned handles close exactly once through borrower cleanup; borrowed handles remain open and answer SELECT 1 after borrower cleanup. Added a fixed-iteration setup benchmark.

Isolation: no cross-test fixture sharing was introduced. Each NewRBETestEnv still creates its own database, Redis fixture, randomized groups/users/API keys, and test-local state. Only apps in the same harness share SQL, exactly as before. Existing invocation IDs and scenarios are unchanged. Harness cleanup stops apps before the owner closes SQL. No assertions, scenarios, tags, timeouts, or existing tests changed.

Executors, proxies, and local-cache environments retain private databases. I did not extend borrowing to these environments because this task does not require proving they have no private SQL state. All committed edits are within the three allowed directories. No dependency/lock/CI changes.

## Measurements (BuildBuddy Remote Bazel throughout)

Single full integration runs used the same command: `bb remote test //enterprise/server/test/integration/remote_execution:remote_execution_test --nocache_test_results --test_output=all`, retaining 11 shards. Elapsed time is Bazel's reported invocation time; summed case time is the sum of Go PASS/FAIL durations (includes test cleanup). Builds, remote runner queues, and scheduling differ; this is not a controlled end-to-end performance estimate.

| Measurement | Before | After, first single run | After, confirmation |
| --- | ---: | ---: | ---: |
| Bazel elapsed seconds | 255.484 | 204.466 | 264.891 |
| Summed case seconds | 150.21 | 158.81 | 168.48 |
| Auto-migrating DB log lines | 215 | 148 | 148 |
| Integration cases passing | 59/59 | 58/59 | 59/59 |

67 discarded app DB migrations were eliminated in each full suite. **No end-to-end speedup was established.** The DB setup saving itself is real, measured separately below; it is small relative to the suite and scheduling variance.

Direct comparison uses the retained old default setup path versus the new borrowed path in the same remote benchmark binary. Both initialize the same testenv services, differing only in DB ownership/setup. A shared owner is initialized outside the subbenchmarks; cleanup is deferred by testing.B in both paths. Command: `bb remote test //server/testutil/testenv:testenv_test --nocache_test_results --test_arg=-test.run=^$ --test_arg=-test.bench=BenchmarkDBHandleSetup --test_arg=-test.benchtime=20x --test_arg=-test.count=5 --test_output=all`.

| Sample (20 iterations each) | Create + migrate, ms/op | Borrow, ms/op |
| --- | ---: | ---: |
| 1 | 19.551137 | 0.121785 |
| 2 | 18.436336 | 0.115926 |
| 3 | 18.149048 | 0.118657 |
| 4 | 15.969823 | 0.124614 |
| 5 | 16.512570 | 0.112041 |
| Median | 18.149048 | 0.118657 |

Measured median difference: 18.030391 ms/app setup (~99.35% less setup time in this benchmark). Multiplying by the 67 observed app starts projects ~1.21 seconds saved in aggregate setup; this is a projection, not observed suite wall-clock improvement. Database creation/migration coverage remains in the harness owner's setup and the default testenv users.

## Flakiness and validation

The first single after run failed TestSimpleCommand_Timeout_StdoutStderrStillVisible at remote_execution_test.go:205: GetCachedExecuteResponse returned NotFound for the cached ActionResult, after command timeout. No assertion or wait was changed to hide this. A subsequent full suite passed. Focused checks using `--test_filter=TestSimpleCommand_Timeout_StdoutStderrStillVisible --runs_per_test=20 --nocache_test_results --test_output=all` passed 20/20 at baseline and 20/20 after. These checks do not prove the single failure was pre-existing or exclude a flakiness regression; its cause remains unresolved.

Baseline runs used a temporary worktree at the original commit for the focused test and sample users. The baseline full integration run used the original clean main worktree. All runs used Remote Bazel; no local fallback was needed.

## Test-case lists from Bazel Go test output

Lists include every PASS case, including reported subtests. Before and after are shown explicitly; set equality was checked by script.

### remote_execution_test before (59)

```text
TestActionMerging_CancellationDoesntAffectMergedActions
TestActionMerging_ClaimingAppDies
TestActionMerging_DisabledWithDoNotCache
TestActionMerging_Hedging
TestActionMerging_ScheduledConcurrently
TestActionMerging_Success
TestActionResultCacheWithFailedAction
TestActionResultCacheWithSuccessfulAction
TestAppShutdownDuringExecution_LeaseTaskRetried
TestAppShutdownDuringExecution_PublishOperationRetried
TestBasicActionIO
TestCommandWithMissingInputRootDigest
TestComplexActionIO
TestComplexActionIOWithCompression
TestContainerRegistryBypass
TestCustomResources
TestCustomResources/respects_custom_resources_if_measured_size_is_available
TestCustomResources/respects_custom_resources_if_measured_size_is_unavailable
TestExternallyRetriedTask_OOMError_MemoryEstimateIncreasedForNextAttempt
TestExternallyRetriedTask_OOMError_MemoryEstimateIncreasedForNextAttempt/task_exceeded_its_memory_estimate
TestExternallyRetriedTask_OOMError_MemoryEstimateIncreasedForNextAttempt/task_stayed_within_its_memory_estimate
TestInvalidOutputs
TestInvalidOutputs/OutputDirectories
TestInvalidOutputs/OutputFiles
TestInvalidOutputs/OutputPaths
TestInvocationCancellation
TestInvocationCancellation/executions_stored_in_DB
TestInvocationCancellation/executions_stored_in_redis
TestOutputDirectoriesAndFiles
TestOutputPaths
TestOutputPathsDirectoriesAndFiles
TestProactiveCancellation
TestRedisAvailabilityMonitoring
TestRedisRestart
TestSaturateTaskQueue
TestSimpleCommandWithExecutorAuthorizationEnabled
TestSimpleCommandWithMultipleExecutors
TestSimpleCommandWithOSArchPool_CaseInsensitive
TestSimpleCommandWithPoolSelectionViaHeader
TestSimpleCommandWithPoolSelectionViaPlatformProp_Failure
TestSimpleCommandWithPoolSelectionViaPlatformProp_Success
TestSimpleCommandWithZeroExitCode
TestSimpleCommand_Abort_ReturnsExecutionError
TestSimpleCommand_DefaultWorkspacePermissions
TestSimpleCommand_NonrootWorkspacePermissions
TestSimpleCommand_RunnerReuse_CanReadPreviouslyWrittenFileButNotOutputDirs
TestSimpleCommand_RunnerReuse_MultipleExecutors_RoutesCommandToSameExecutor
TestSimpleCommand_RunnerReuse_PoolSelectionViaHeader_RoutesCommandToSameExecutor
TestSimpleCommand_RunnerReuse_ReLinksFilesFromDuplicateInputs
TestSimpleCommand_Timeout_StdoutStderrStillVisible
TestTaskReservationsNotLostOnExecutorShutdown
TestTaskReservationsNotLostWithMultipleExecutorsShuttingDown
TestTerminationGracePeriod
TestTimeoutAlwaysReturnsDeadlineExceeded
TestUnregisterExecutor
TestWaitExecution
TestWorkSchedulingOnNewExecutor
TestWorkflowCommand_ExecutorShutdown_RetriedByScheduler
TestWorkflowCommand_InternalError_RetriedByScheduler
```

### remote_execution_test after (59; identical)

```text
TestActionMerging_CancellationDoesntAffectMergedActions
TestActionMerging_ClaimingAppDies
TestActionMerging_DisabledWithDoNotCache
TestActionMerging_Hedging
TestActionMerging_ScheduledConcurrently
TestActionMerging_Success
TestActionResultCacheWithFailedAction
TestActionResultCacheWithSuccessfulAction
TestAppShutdownDuringExecution_LeaseTaskRetried
TestAppShutdownDuringExecution_PublishOperationRetried
TestBasicActionIO
TestCommandWithMissingInputRootDigest
TestComplexActionIO
TestComplexActionIOWithCompression
TestContainerRegistryBypass
TestCustomResources
TestCustomResources/respects_custom_resources_if_measured_size_is_available
TestCustomResources/respects_custom_resources_if_measured_size_is_unavailable
TestExternallyRetriedTask_OOMError_MemoryEstimateIncreasedForNextAttempt
TestExternallyRetriedTask_OOMError_MemoryEstimateIncreasedForNextAttempt/task_exceeded_its_memory_estimate
TestExternallyRetriedTask_OOMError_MemoryEstimateIncreasedForNextAttempt/task_stayed_within_its_memory_estimate
TestInvalidOutputs
TestInvalidOutputs/OutputDirectories
TestInvalidOutputs/OutputFiles
TestInvalidOutputs/OutputPaths
TestInvocationCancellation
TestInvocationCancellation/executions_stored_in_DB
TestInvocationCancellation/executions_stored_in_redis
TestOutputDirectoriesAndFiles
TestOutputPaths
TestOutputPathsDirectoriesAndFiles
TestProactiveCancellation
TestRedisAvailabilityMonitoring
TestRedisRestart
TestSaturateTaskQueue
TestSimpleCommandWithExecutorAuthorizationEnabled
TestSimpleCommandWithMultipleExecutors
TestSimpleCommandWithOSArchPool_CaseInsensitive
TestSimpleCommandWithPoolSelectionViaHeader
TestSimpleCommandWithPoolSelectionViaPlatformProp_Failure
TestSimpleCommandWithPoolSelectionViaPlatformProp_Success
TestSimpleCommandWithZeroExitCode
TestSimpleCommand_Abort_ReturnsExecutionError
TestSimpleCommand_DefaultWorkspacePermissions
TestSimpleCommand_NonrootWorkspacePermissions
TestSimpleCommand_RunnerReuse_CanReadPreviouslyWrittenFileButNotOutputDirs
TestSimpleCommand_RunnerReuse_MultipleExecutors_RoutesCommandToSameExecutor
TestSimpleCommand_RunnerReuse_PoolSelectionViaHeader_RoutesCommandToSameExecutor
TestSimpleCommand_RunnerReuse_ReLinksFilesFromDuplicateInputs
TestSimpleCommand_Timeout_StdoutStderrStillVisible
TestTaskReservationsNotLostOnExecutorShutdown
TestTaskReservationsNotLostWithMultipleExecutorsShuttingDown
TestTerminationGracePeriod
TestTimeoutAlwaysReturnsDeadlineExceeded
TestUnregisterExecutor
TestWaitExecution
TestWorkSchedulingOnNewExecutor
TestWorkflowCommand_ExecutorShutdown_RetriedByScheduler
TestWorkflowCommand_InternalError_RetriedByScheduler
```

### Sample users before (13; invocationdb and memory_cache)

```text
TestAtimeUpdater
TestAttemptLogic
TestAuthenticatedInvocationReplacesMissingAPIKeyPlaceholder
TestCreateReadUpdateDelete
TestDeleteInvocations
TestDeleteInvocationsRollsBackOnFailure
TestGetSet
TestIsolation
TestLRU
TestMultiGetSet
TestReadOffsetLimit
TestReadWrite
TestSizeLimit
```

### Sample users after (13; identical)

```text
TestAtimeUpdater
TestAttemptLogic
TestAuthenticatedInvocationReplacesMissingAPIKeyPlaceholder
TestCreateReadUpdateDelete
TestDeleteInvocations
TestDeleteInvocationsRollsBackOnFailure
TestGetSet
TestIsolation
TestLRU
TestMultiGetSet
TestReadOffsetLimit
TestReadWrite
TestSizeLimit
```

### Added ownership test cases after (5; coverage superset)

```text
TestDBHandleOwnership
TestDBHandleOwnership/borrowed
TestDBHandleOwnership/borrowed/cleanup
TestDBHandleOwnership/owned
TestDBHandleOwnership/owned/cleanup
```

## Repeated-run counts

Command: `bb remote test //enterprise/server/test/integration/remote_execution:remote_execution_test //server/testutil/testenv:testenv_test //server/backends/invocationdb:invocationdb_test //server/backends/memory_cache:memory_cache_test --nocache_test_results --runs_per_test=5 --test_output=all`.

| Target | Pass count | Case observations |
| --- | ---: | ---: |
| remote_execution_test | 5/5 complete suites (55 shard executions) | 295/295 (59 × 5) |
| invocationdb_test | 5/5 | All cases 5/5 |
| memory_cache_test | 5/5 | All cases 5/5 |
| testenv_test | 5/5 | 25/25 including nested ownership cases |

The two sample users together have 13 cases and 65/65 repeated case observations. The combined output contains 77 distinct cases, each passing exactly five times (385/385). The complete five-run invocation took 1060.189 seconds including build/queue/scheduling; it is not compared with the one-run baseline. The confirmation after suite preserved the exact 59-case baseline set. Across all after executions, the timeout case passed 26/27 (one failure); baseline passed 21/21. This distinction is retained rather than reporting only the successful reruns.

## Evidence

Raw logs are retained locally at `/tmp/rbe-before.log`, `/tmp/rbe-after-single.log`, `/tmp/rbe-after-confirm.log`, `/tmp/rbe-after.log`, `/tmp/rbe-benchmark.log`, `/tmp/rbe-baseline-flake.log`, `/tmp/rbe-after-flake.log`, `/tmp/rbe-sample-before.log`, and `/tmp/rbe-sample-reference.log`.

- before: https://app.buildbuddy.io/invocation/797ee80d-04fb-46f4-a5af-6c45c4276cb3
- after-single: https://app.buildbuddy.io/invocation/543777f9-2f86-459a-9458-9c34493b9a37
- after-confirm: https://app.buildbuddy.io/invocation/76e70722-e0ff-412b-8da6-09498cc6f073
- after: https://app.buildbuddy.io/invocation/974b398d-892f-4af7-a59a-96bd555d4c00
- benchmark: https://app.buildbuddy.io/invocation/7a4c3b39-fc0d-40e2-ad55-fcbb46ac1b9d
- baseline-flake: https://app.buildbuddy.io/invocation/aec30b09-054a-4c88-afd9-eb844d7d2077
- after-flake: https://app.buildbuddy.io/invocation/eb629e41-4ee2-41a7-aab9-de3a6c65a25a
- sample-before: https://app.buildbuddy.io/invocation/fb456ff1-d646-4904-a744-16c0a2f94afd
- sample-reference: https://app.buildbuddy.io/invocation/47851af8-5723-4a05-a13d-d7a0591416b7

`gofmt` and `git diff --check` passed. result.md is ignored and excluded from the commit. Fetch origin restored to the internal mirror after remote testing; push origin remains the internal mirror. No push or PR was made.

