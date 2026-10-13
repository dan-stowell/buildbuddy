# Reduce polling delays in workflow, hit-tracker, and build-log tests

Workflow tests slept for a second before checking scheduled rows, hit-tracker delivery tests used the production 250 ms update interval, and build-log readiness checks allowed exponentially growing pauses. This change removes the redundant workflow sleep and uses shorter polling intervals while preserving the existing conditions, exact assertions, and test scenarios.

- Workflow: poll scheduled-row conditions every 10 ms instead of 100 ms; preserve their one-second condition timeout.
- Hit tracker: set a 10 ms update interval in ordinary real-clock test setup and observe delivery every 10 ms. Preserve fake-clock control, the negative-observation sleep, and exact hit/byte assertions.
- Build logs: cap readiness and log polling pauses at 10 ms, with explicit two-second deadlines. Preserve immediate failure for unexpected RPC errors and the final exact log diff. The old sleep series totaled about 1.049 seconds plus RPC/scheduling overhead; the new budget gives readiness more time.

## Scope and commits

The final branch contains exactly three commits:

- `535fa3df6` — (a) workflow waits.
- `84d5a05c6` — (c) hit-tracker intervals.
- `aaed51957` — (e) build-log waits.

**(b) reverted entirely:** removed the barrier commit from branch history and replayed (c)/(e). The container test now matches `origin/master` byte for byte, including its original one-second fake pull delay. The proposed `IsolationType` signal occurred before mutex acquisition and could allow a serialization regression to escape detection; its passing runs did not establish equivalent regression coverage. Chose the reviewer-authorized revert rather than retaining that barrier. No serialization mutation experiment was performed or claimed.

**(d) unchanged:** the discarded 100 ms refresh candidate passed five browser suite runs but did not show a measured saving: affected case 20.50 s before versus 20.60 s candidate median (18.25–22.08 s). Whole-case browser/server/Bazel setup noise prevents attributing those timings to refresh pauses. No candidate change is retained.

**(f) unchanged:** the existing manual/no-sandbox model test requires an external TensorFlow serving instance and features config, and its prediction examples are empty. No configured model was available to substantiate a routine-test saving. `TestSamplePrediction` was not run; no pass is claimed.

## Validation

Final branch validation used BuildBuddy Remote Bazel from a login shell:

```sh
bash -lc 'cd ~/repo && bb remote test //enterprise/server/workflow/service:service_test //enterprise/server/remote_execution/container:container_test //enterprise/server/hit_tracker_client:hit_tracker_client_test //enterprise/server/test/integration/build_logs:build_logs_test --test_output=all --nocache_test_results --runs_per_test=5'
```

[Final validation invocation](https://app.buildbuddy.io/invocation/7b34b1b2-6ee0-4670-88d3-ec0c47831353): **4/4 suites passed**, exit status 0. Workflow passed 30/30 shard executions; container, hit tracker, and build logs each passed 5/5 executions. All **95 distinct reported cases**, including parent and nested cases, passed **5/5**; their baseline and final sets are identical. All assertions, cases, and scenarios remain. `git diff --check` passed.

No local test fallback was needed. Final remote logs: `/tmp/small-waits/final.log`; parsed case/timing data: `/tmp/small-waits/final-parsed.json`.

## Before/after timing evidence

The same measurement method was used before and after: uncached remote Bazel verbose Go `--- PASS: Test… (Xs)` durations, including setup/cleanup and rounded to 0.01 seconds. Baseline is one original-code run at `770b88bc89c0b20059d42a237e8c7923ff5b7215`; final values are medians of five final-branch runs. This is not an isolated measurement of polling time.

| Changed suite, sum of top-level durations | Before | Final, summed case medians | Difference |
| --- | ---: | ---: | ---: |
| `//enterprise/server/hit_tracker_client:hit_tracker_client_test` | 2.50 s | 0.62 s | 1.88 s |
| `//enterprise/server/workflow/service:service_test` | 10.23 s | 6.47 s | 3.76 s |
| `//enterprise/server/test/integration/build_logs:build_logs_test` | 6.44 s | 2.59 s | 3.85 s |

The changed-suite case-duration sum was **19.17 s before → 9.68 s final (9.49 s lower)**. Setup and scheduling noise contribute to this difference; it cannot all be attributed to the shorter waits. Shards execute concurrently, so the sum is not an end-to-end Bazel speedup.

| Case | Before | Final median | Final range |
| --- | ---: | ---: | ---: |
| `TestCreateScheduledWorkflow` | 1.05 s | 0.05 s | 0.05–0.06 s |
| `TestCASHitTracker` | 0.35 s | 0.04 s | 0.04–0.08 s |
| `TestBuildLogs_CompletedInvocation` | 4.25 s | 1.26 s | 1.23–1.28 s |
| `TestBuildLogs_InProgressInvocation` | 2.19 s | 1.33 s | 1.22–1.33 s |
| `TestPullImageIfNecessary_ParallelCallsSerialized` | 1.03 s | 1.03 s | 1.02–1.03 s |

The container case is restored and unchanged; its timings are provided only to confirm the original test runs again. Its discarded barrier speedup is excluded from the retained-change timing totals.

Baseline combined Bazel elapsed time was 114.361 s for one repetition (including a failed browser startup); final elapsed time was 114.111 s for five repetitions of the four suites above. These invocation workloads differ, so their wall times do not establish a speedup.

[Original baseline invocation](https://app.buildbuddy.io/invocation/da651fd2-3fec-4ea8-a349-7b3a245fed67), log `/tmp/small-waits/before.log`. That invocation passed all four suites listed above; its overall exit status was 3 because the unchanged browser tests could not launch chromedriver without `libxcb.so.1` on the default runner. This failure is not counted as a passing browser run.

For the discarded browser candidate, the BUILD rule’s browser image could not start as the whole runner because it lacked the expected `buildbuddy` user. Using `--config=remote --remote_executor=grpcs://remote.buildbuddy.io --remote_cache=grpcs://remote.buildbuddy.io` let test actions use the proper browser image: [original browser baseline](https://app.buildbuddy.io/invocation/43becc53-e8e5-446e-b2ee-2dc38b97b235) passed 3/3 cases, and the [discarded candidate](https://app.buildbuddy.io/invocation/6644ee6a-94a0-4ca4-bc4c-48f4128eb352) passed 15/15 case executions. This evidence belongs to the premise check for (d), not to the retained changes.

## PR preparation checks

- Audited every tree in `git rev-list origin/master..HEAD` for a file whose basename is `result.md`, at any directory depth: none in any of the three commits. None was tracked before this revision, so no `git rm --cached` was needed. The on-disk root write-up was retained and updated.
- `git diff --name-only origin/master...HEAD` lists only:

```text
enterprise/server/hit_tracker_client/hit_tracker_client_test.go
enterprise/server/test/integration/build_logs/build_logs_test.go
enterprise/server/workflow/service/service_test.go
```

- Origin fetch was temporarily set to the public GitHub fork for remote validation, with push kept internal. Both URLs are now `https://github.int.exe.xyz/dan-stowell/buildbuddy.git`.
- `/home/exedev/repo/result.md` is an ignored, untracked working file and is not committed. No push or PR was made.

## Before and after test-case lists

Extracted from Bazel verbose test output. Each list includes parent/nested cases. Before cases passed 1/1; final cases passed 5/5. Sets match exactly for every suite. Timing sums above count top-level cases only to avoid double-counting children.

### //enterprise/server/remote_execution/container:container_test

Before (1/1 each):

```text
TestImageCacheAuthenticator
TestImagePullMetricStatus
TestImagePullMetricStatus/nil
TestImagePullMetricStatus/not_found
TestImagePullMetricStatus/permission_denied
TestImagePullMetricStatus/resource_exhausted
TestImagePullMetricStatus/unavailable
TestImagePullMetricStatus/unknown_error
TestPullImageIfNecessary_InvalidCredentials_PermissionDenied
TestPullImageIfNecessary_NonOCIFetcherPullErrors
TestPullImageIfNecessary_NonOCIFetcherPullErrors/not_found/use_oci_fetcher_false
TestPullImageIfNecessary_NonOCIFetcherPullErrors/not_found/use_oci_fetcher_true
TestPullImageIfNecessary_NonOCIFetcherPullErrors/permission_denied/use_oci_fetcher_false
TestPullImageIfNecessary_NonOCIFetcherPullErrors/permission_denied/use_oci_fetcher_true
TestPullImageIfNecessary_NonOCIFetcherPullErrors/resource_exhausted/use_oci_fetcher_false
TestPullImageIfNecessary_NonOCIFetcherPullErrors/resource_exhausted/use_oci_fetcher_true
TestPullImageIfNecessary_NonOCIFetcherPullErrors/unavailable/use_oci_fetcher_false
TestPullImageIfNecessary_NonOCIFetcherPullErrors/unavailable/use_oci_fetcher_true
TestPullImageIfNecessary_ParallelCallsSerialized
TestPullImageIfNecessary_ValidCredentials
TestTracedCommandContainer_PostCompletionStats_PreservesDelegateStats
TestTracedCommandContainer_PostCompletionStats_ResetOnRunAndExec
TestTracedCommandContainer_PostCompletionStats_TimesPause
TestUsageStats
TestUsageStats_BasicTaskStatsOmitsTimeline
TestUsageStats_ConcurrentUpdateAndBasicTaskStats
TestUsageStats_GPUTimeseries
TestUsageStats_GPUUsageReportsPeaks
TestUsageStats_Timeseries
TestUsageStats_ZeroGPUUsageOmitted
```

Final (5/5 each):

```text
TestImageCacheAuthenticator
TestImagePullMetricStatus
TestImagePullMetricStatus/nil
TestImagePullMetricStatus/not_found
TestImagePullMetricStatus/permission_denied
TestImagePullMetricStatus/resource_exhausted
TestImagePullMetricStatus/unavailable
TestImagePullMetricStatus/unknown_error
TestPullImageIfNecessary_InvalidCredentials_PermissionDenied
TestPullImageIfNecessary_NonOCIFetcherPullErrors
TestPullImageIfNecessary_NonOCIFetcherPullErrors/not_found/use_oci_fetcher_false
TestPullImageIfNecessary_NonOCIFetcherPullErrors/not_found/use_oci_fetcher_true
TestPullImageIfNecessary_NonOCIFetcherPullErrors/permission_denied/use_oci_fetcher_false
TestPullImageIfNecessary_NonOCIFetcherPullErrors/permission_denied/use_oci_fetcher_true
TestPullImageIfNecessary_NonOCIFetcherPullErrors/resource_exhausted/use_oci_fetcher_false
TestPullImageIfNecessary_NonOCIFetcherPullErrors/resource_exhausted/use_oci_fetcher_true
TestPullImageIfNecessary_NonOCIFetcherPullErrors/unavailable/use_oci_fetcher_false
TestPullImageIfNecessary_NonOCIFetcherPullErrors/unavailable/use_oci_fetcher_true
TestPullImageIfNecessary_ParallelCallsSerialized
TestPullImageIfNecessary_ValidCredentials
TestTracedCommandContainer_PostCompletionStats_PreservesDelegateStats
TestTracedCommandContainer_PostCompletionStats_ResetOnRunAndExec
TestTracedCommandContainer_PostCompletionStats_TimesPause
TestUsageStats
TestUsageStats_BasicTaskStatsOmitsTimeline
TestUsageStats_ConcurrentUpdateAndBasicTaskStats
TestUsageStats_GPUTimeseries
TestUsageStats_GPUUsageReportsPeaks
TestUsageStats_Timeseries
TestUsageStats_ZeroGPUUsageOmitted
```

### //enterprise/server/hit_tracker_client:hit_tracker_client_test

Before (1/1 each):

```text
TestACHitTracker
TestACHitTracker_SkipRemote
TestBatchAndSendAreSerialized
TestCASHitTracker
TestCASHitTracker_AcceptsHitsWhileSplitOverflowIsPending
TestCASHitTracker_DropsUpdates
TestCASHitTracker_NoDeduplication
TestCASHitTracker_SkipRemote
TestCASHitTracker_SplitsUpdates
TestRegister
TestRegister/no_remote_cache_disables_hit_tracking
TestRegister/remote_cache_and_workers_enable_hit_tracking
TestRegister/zero_workers_disables_hit_tracking
```

Final (5/5 each):

```text
TestACHitTracker
TestACHitTracker_SkipRemote
TestBatchAndSendAreSerialized
TestCASHitTracker
TestCASHitTracker_AcceptsHitsWhileSplitOverflowIsPending
TestCASHitTracker_DropsUpdates
TestCASHitTracker_NoDeduplication
TestCASHitTracker_SkipRemote
TestCASHitTracker_SplitsUpdates
TestRegister
TestRegister/no_remote_cache_disables_hit_tracking
TestRegister/remote_cache_and_workers_enable_hit_tracking
TestRegister/zero_workers_disables_hit_tracking
```

### //enterprise/server/workflow/service:service_test

Before (1/1 each):

```text
TestAPIDispatch_ActionFiltering
TestCreateScheduledWorkflow
TestCreateScheduledWorkflow_ConcurrentPushes
TestCreateScheduledWorkflow_MultipleSchedules
TestDeleteLegacyWorkflow
TestExecuteWorkflow_BlockedGroupDoesNotStartWorkflow
TestExecuteWorkflow_CrossOrgAccessDenied
TestExecuteWorkflow_CrossOrgAccessDenied/existing_repo
TestExecuteWorkflow_CrossOrgAccessDenied/nonexistent_repo
TestListLegacyWorkflows
TestScheduledWorkflow
TestScheduledWorkflow_ConcurrentServers
TestScheduledWorkflow_DispatchFailure
TestScheduledWorkflow_FailedMaxAttempts
TestScheduledWorkflow_GitProviderError_Retries
TestScheduledWorkflow_MaxConsecutiveFailures
TestScheduledWorkflow_NoEligibleSchedules
TestScheduledWorkflow_SharesInstanceNameWithWebhookEvent
TestScheduledWorkflow_StaleCronDeletesSchedule
TestScheduledWorkflow_StaleCronDeletesSchedule/mismatched_cron_in_config
TestScheduledWorkflow_StaleCronDeletesSchedule/no_config_at_all
TestScheduledWorkflow_StaleCronDeletesSchedule/no_schedule_trigger_in_config
TestScheduledWorkflow_SuppressedByExperiment
TestScheduledWorkflows_ConfigValidation_InvalidCron
TestScheduledWorkflows_ConfigValidation_TooFrequentCron
TestTimeout
TestTimeout/timeout_configured_in_experiment
TestTimeout/timeout_not_configured_in_experiment
TestUpdateScheduledWorkflows
TestUpdateScheduledWorkflows_ConfigFileDeleted
TestUpdateScheduledWorkflows_SkipsNonDefaultBranchPush
TestUpdateScheduledWorkflows_SkipsWithoutConfigChange
TestWebhook_FailedToStart_PublishesStatus
TestWebhook_LegacyWorkflow_UseDefaultWorkflowConfig
TestWebhook_NoWorkflowConfig_CodesearchEnabled_StartsIndexing
TestWebhook_NoWorkflowConfig_NOP
TestWebhook_RespectsVisibility
TestWebhook_SlashCommandFromUntrustedCommenter_NOP
TestWebhook_SlashCommandSuppressedByExperiment
TestWebhook_TrustedApprovalOnAlreadyTrustedPullRequest_NOP
TestWebhook_TrustedApprovalOnUntrustedPullRequest_StartsTrustedWorkflow
TestWebhook_TrustedPullRequest_StartsTrustedWorkflow
TestWebhook_TrustedPush_StartsTrustedWorkflow
TestWebhook_TrustedSlashCommandOnTrustedPullRequest_StartsTrustedWorkflow
TestWebhook_TrustedSlashCommandOnUntrustedForkPullRequest_StartsUntrustedWorkflow
TestWebhook_TrustedTagPush_StartsTrustedWorkflow
TestWebhook_UntrustedApprovalOnUntrustedPullRequest_NOP
TestWebhook_UntrustedPullRequest_StartsUntrustedWorkflow
TestWebhook_UseDefaultWorkflowConfig
TestWebhook_WorkflowSuppressedByExperiment
```

Final (5/5 each):

```text
TestAPIDispatch_ActionFiltering
TestCreateScheduledWorkflow
TestCreateScheduledWorkflow_ConcurrentPushes
TestCreateScheduledWorkflow_MultipleSchedules
TestDeleteLegacyWorkflow
TestExecuteWorkflow_BlockedGroupDoesNotStartWorkflow
TestExecuteWorkflow_CrossOrgAccessDenied
TestExecuteWorkflow_CrossOrgAccessDenied/existing_repo
TestExecuteWorkflow_CrossOrgAccessDenied/nonexistent_repo
TestListLegacyWorkflows
TestScheduledWorkflow
TestScheduledWorkflow_ConcurrentServers
TestScheduledWorkflow_DispatchFailure
TestScheduledWorkflow_FailedMaxAttempts
TestScheduledWorkflow_GitProviderError_Retries
TestScheduledWorkflow_MaxConsecutiveFailures
TestScheduledWorkflow_NoEligibleSchedules
TestScheduledWorkflow_SharesInstanceNameWithWebhookEvent
TestScheduledWorkflow_StaleCronDeletesSchedule
TestScheduledWorkflow_StaleCronDeletesSchedule/mismatched_cron_in_config
TestScheduledWorkflow_StaleCronDeletesSchedule/no_config_at_all
TestScheduledWorkflow_StaleCronDeletesSchedule/no_schedule_trigger_in_config
TestScheduledWorkflow_SuppressedByExperiment
TestScheduledWorkflows_ConfigValidation_InvalidCron
TestScheduledWorkflows_ConfigValidation_TooFrequentCron
TestTimeout
TestTimeout/timeout_configured_in_experiment
TestTimeout/timeout_not_configured_in_experiment
TestUpdateScheduledWorkflows
TestUpdateScheduledWorkflows_ConfigFileDeleted
TestUpdateScheduledWorkflows_SkipsNonDefaultBranchPush
TestUpdateScheduledWorkflows_SkipsWithoutConfigChange
TestWebhook_FailedToStart_PublishesStatus
TestWebhook_LegacyWorkflow_UseDefaultWorkflowConfig
TestWebhook_NoWorkflowConfig_CodesearchEnabled_StartsIndexing
TestWebhook_NoWorkflowConfig_NOP
TestWebhook_RespectsVisibility
TestWebhook_SlashCommandFromUntrustedCommenter_NOP
TestWebhook_SlashCommandSuppressedByExperiment
TestWebhook_TrustedApprovalOnAlreadyTrustedPullRequest_NOP
TestWebhook_TrustedApprovalOnUntrustedPullRequest_StartsTrustedWorkflow
TestWebhook_TrustedPullRequest_StartsTrustedWorkflow
TestWebhook_TrustedPush_StartsTrustedWorkflow
TestWebhook_TrustedSlashCommandOnTrustedPullRequest_StartsTrustedWorkflow
TestWebhook_TrustedSlashCommandOnUntrustedForkPullRequest_StartsUntrustedWorkflow
TestWebhook_TrustedTagPush_StartsTrustedWorkflow
TestWebhook_UntrustedApprovalOnUntrustedPullRequest_NOP
TestWebhook_UntrustedPullRequest_StartsUntrustedWorkflow
TestWebhook_UseDefaultWorkflowConfig
TestWebhook_WorkflowSuppressedByExperiment
```

### //enterprise/server/test/integration/build_logs:build_logs_test

Before (1/1 each):

```text
TestBuildLogs_CompletedInvocation
TestBuildLogs_InProgressInvocation
```

Final (5/5 each):

```text
TestBuildLogs_CompletedInvocation
TestBuildLogs_InProgressInvocation
```

### Unchanged browser cases

Original baseline passed 1/1 each; discarded candidate passed 5/5 each. Case names were identical:

```text
TestAuthenticatedInvocation_CacheEnabled
TestAuthenticatedInvocation_PersonalAPIKey_CacheEnabled
TestInvocationWithRemoteExecution
```

### Unchanged manual model case

`TestSamplePrediction` is declared before and after. It was not run or listed by an executed binary because the required model setup was unavailable.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3