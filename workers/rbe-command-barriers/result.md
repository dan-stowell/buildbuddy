# Explicit action gates in remote_execution integration tests

Only `enterprise/server/test/integration/remote_execution/remote_execution_test.go` changed. Both actions now write a readiness marker and block until the test creates a release file, polling every 10 ms. Each test waits for readiness with a bounded 30-second timeout. Redis restart releases its action after the victim shard restarts. Merged cancellation waits for `bep.Finish()` to acknowledge processing the queued interruption event before releasing its action. The existing merge-ID, exit-code, Redis-task selection, and successful-Bazel-build assertions remain.

The executors run locally within the integration-test environment and share its filesystem. Gates are isolated by `t.TempDir()`. The rbetest harness and short lease tests were not edited; no cases, assertions, or scenarios were removed, skipped, or weakened.

## Timings and repeated runs

All measurements used **BuildBuddy Remote Bazel**, with per-case durations from the Go PASS output. Both versions used the same target, filter, and uncached test execution. The baseline ran once; the final version ran five times with `--runs_per_test=5`. Build/analysis/runner startup are excluded from these per-case timings.

| Case | Before (s) | After, five runs (s) | After median (s) | Median saving (s) | Passes |
|---|---:|---|---:|---:|---|
| TestRedisRestart | 14.95 | 8.25, 8.39, 8.24, 8.59, 7.95 | 8.25 | 6.70 | before 1/1; after 5/5 |
| TestActionMerging_CancellationDoesntAffectMergedActions | 5.78 | 0.93, 0.76, 0.77, 0.84, 0.73 | 0.77 | 5.01 | before 1/1; after 5/5 |

The premise is supported: both original commands contain `sleep 5`, and merged cancellation loses approximately five seconds after gating. Redis restart also includes Bazel and service setup, so its measured 6.70-second difference includes environment/setup variation; it should not be interpreted as a guaranteed saving larger than the removed five-second sleep. Five repetitions provide useful regression evidence, not proof that flakiness is impossible.

Commands (run with `bash -lc`):

```sh
bb remote test //enterprise/server/test/integration/remote_execution:remote_execution_test --test_filter='TestRedisRestart|TestActionMerging_CancellationDoesntAffectMergedActions' --test_output=all --nocache_test_results
# Final version: same command plus --runs_per_test=5.
bb remote test //enterprise/server/test/integration/remote_execution:remote_execution_test --test_output=all --nocache_test_results
# Additional evidence run with increased output limit, because Bazel suppressed shard 1's >1 MB log:
bb remote test //enterprise/server/test/integration/remote_execution:remote_execution_test --test_output=all --experimental_ui_max_stdouterr_bytes=10000000
# Before-list command in a separate checkout of the original revision:
bb remote test //enterprise/server/test/integration/remote_execution:remote_execution_test --test_arg=-test.list=. --test_sharding_strategy=disabled --test_output=all
```

The final full-target uncached run passed, with no failures or skips. Its shard-1 log was suppressed by Bazel's default output limit; the additional evidence run supplies explicit PASS lines for those five cases. The additional evidence run also finished successfully and explicitly shows PASS for all 55 top-level cases and all 9 nested cases, with no failures or skips. The full original suite was listed, not executed; the two affected cases were executed before the change.

## Before/after case lists

Original revision `770b88bc89c0b20059d42a237e8c7923ff5b7215`: 55 names from Bazel's Go `-test.list=.` output. Final revision: 55 top-level names from Bazel's Go PASS output. The sets are exactly equal (no additions or removals), and every final case passes.

Before:

```text
TestActionMerging_CancellationDoesntAffectMergedActions
TestActionMerging_ClaimingAppDies
TestActionMerging_DisabledWithDoNotCache
TestActionMerging_Hedging
TestActionMerging_LongTask
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
TestExternallyRetriedTask_OOMError_MemoryEstimateIncreasedForNextAttempt
TestInvalidOutputs
TestInvocationCancellation
TestManySimpleCommandsWithMultipleExecutors
TestMultipleSchedulersAndExecutors
TestOutputDirectoriesAndFiles
TestOutputPaths
TestOutputPathsDirectoriesAndFiles
TestProactiveCancellation
TestRedisAvailabilityMonitoring
TestRedisRestart
TestSaturateTaskQueue
TestSimpleCommandWithExecutorAuthorizationEnabled
TestSimpleCommandWithMultipleExecutors
TestSimpleCommandWithNonZeroExitCode
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
TestSimpleCommand_RunnerReuse_ReLinksFilesFromFileCache
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

After (each has a PASS result):

```text
TestActionMerging_CancellationDoesntAffectMergedActions
TestActionMerging_ClaimingAppDies
TestActionMerging_DisabledWithDoNotCache
TestActionMerging_Hedging
TestActionMerging_LongTask
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
TestExternallyRetriedTask_OOMError_MemoryEstimateIncreasedForNextAttempt
TestInvalidOutputs
TestInvocationCancellation
TestManySimpleCommandsWithMultipleExecutors
TestMultipleSchedulersAndExecutors
TestOutputDirectoriesAndFiles
TestOutputPaths
TestOutputPathsDirectoriesAndFiles
TestProactiveCancellation
TestRedisAvailabilityMonitoring
TestRedisRestart
TestSaturateTaskQueue
TestSimpleCommandWithExecutorAuthorizationEnabled
TestSimpleCommandWithMultipleExecutors
TestSimpleCommandWithNonZeroExitCode
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
TestSimpleCommand_RunnerReuse_ReLinksFilesFromFileCache
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

Nested cases also passing after:

```text
TestCustomResources/respects_custom_resources_if_measured_size_is_available
TestCustomResources/respects_custom_resources_if_measured_size_is_unavailable
TestExternallyRetriedTask_OOMError_MemoryEstimateIncreasedForNextAttempt/task_exceeded_its_memory_estimate
TestExternallyRetriedTask_OOMError_MemoryEstimateIncreasedForNextAttempt/task_stayed_within_its_memory_estimate
TestInvalidOutputs/OutputDirectories
TestInvalidOutputs/OutputFiles
TestInvalidOutputs/OutputPaths
TestInvocationCancellation/executions_stored_in_DB
TestInvocationCancellation/executions_stored_in_redis
```

## Evidence

- Baseline execution: https://app.buildbuddy.io/invocation/776e1aa2-8a53-4d8e-99b1-204b200a231e
- Final five-repeat execution: https://app.buildbuddy.io/invocation/d2dd4c20-48fd-40fe-b4d1-f090796bb2cb
- Final full-target uncached execution: https://app.buildbuddy.io/invocation/f79280ae-8101-4b81-ac44-1fdb98ff5603
- Final full-target execution with all case logs: https://app.buildbuddy.io/invocation/3b4ef2a5-2627-4a30-8e5c-3b0a0c2936c1
- Original test listing: https://app.buildbuddy.io/invocation/ee659ada-830f-44fe-9f70-7ea3b3e72b3c
- Local logs: `/tmp/remote-gates-before.log`, `/tmp/remote-gates-verified.log`, `/tmp/remote-gates-full-final.log`, `/tmp/remote-gates-full-evidence.log`, `/tmp/remote-gates-before-list.log`.

No local Bazel fallback was needed. No harness changes, pushes, or PRs. This report is git-ignored and is not committed.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3