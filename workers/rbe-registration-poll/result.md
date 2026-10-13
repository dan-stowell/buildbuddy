# Executor registration polling

Changed only `enterprise/server/test/integration/remote_execution/rbetest/rbetest.go`: registration retry sleep from 100 ms to 10 ms. Exact executor ID-set equality, assertions, and the 60-second deadline remain unchanged. No test cases, scenarios, or coverage were removed or weakened.

## Measurements

All tests used BuildBuddy Remote Bazel; no local fallback was needed. Target: `//enterprise/server/test/integration/remote_execution:remote_execution_test` (11 shards).

Matching before/after command:

```sh
bb remote test //enterprise/server/test/integration/remote_execution:remote_execution_test --nocache_test_results --test_output=all --test_arg=-test.v
```

Temporary, identical instrumentation logged each registration call's elapsed time and sleep count. It was removed before the final repeated-run check and is not committed.

| Metric | Before (100 ms) | After (10 ms) |
| --- | ---: | ---: |
| Bazel elapsed time | 288.326 s | 265.034 s |
| Bazel critical path | 113.85 s | 126.63 s |
| Registration calls | 139 | 139 |
| Calls that slept | 3 | 2 |
| Total retry sleeps | 3 | 2 |
| Total registration wait duration | 0.817110 s | 0.445940 s |
| Individual sleeping wait durations | 106.445, 106.207, 105.945 ms | 14.549, 14.476 ms |
| Passing cases (including subtests) | 59 | 59 |

The premise is supported for waits that actually sleep, but the estimate overstates the benefit if applied to all registration calls: 136/139 baseline calls never slept. The baseline predicts about 0.27 s aggregate savings from three 90 ms reductions, not 12.51 s from 139 reductions. Observed aggregate registration time decreased by 0.371170 s; the number of sleeping calls also varied. The 23.292 s reduction in total Bazel elapsed time cannot be attributed to this change, especially since the critical path increased. These runs establish a small polling benefit, not a significant demonstrated whole-target speedup.

Before invocation: https://app.buildbuddy.io/invocation/c0cc1e97-dffb-43b7-a839-38aac97ab594

After invocation: https://app.buildbuddy.io/invocation/002aced1-a853-4b92-bc46-409fbf2af4bf

Raw logs: `/tmp/rbetest-before.log`, `/tmp/rbetest-after.log`, `/tmp/rbetest-repeat.log`.

## Coverage: before and after

Lists below are sorted names extracted from `--- PASS:` lines in verbose Bazel test output, including nested cases. Both contain exactly 59 unique names and are byte-for-byte equal; every listed case passed in both measurements.

Before:

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

After:

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

## Repeated runs

Final, uninstrumented one-line patch passed 5/5 complete target runs: all 59 cases passed exactly five times (295 passing case executions, zero failures). The repeated-run case-name set equals the before/after measurement set. No flakes were observed in these five runs. This is evidence from five runs, not a guarantee against future flakes.

Command:

```sh
bb remote test //enterprise/server/test/integration/remote_execution:remote_execution_test --nocache_test_results --test_output=all --test_arg=-test.v --runs_per_test=5
```

Invocation: https://app.buildbuddy.io/invocation/09cf53d0-f954-4fd1-b42f-8753352747a9

Repeated invocation Bazel elapsed time: 994.058 s; critical path: 204.14 s. This five-run duration is not compared with the single-run measurements.

## Scope and choices

Used 10 ms rather than 5 ms to reduce unnecessary scheduler RPC traffic. Did not change the separate executor-unregistration poll, deadlines, sharding, build configuration, or test scenarios. Only the scoped Go file is included in the commit; this report remains uncommitted. Origin fetch and push URLs were restored to the internal mirror after testing.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3