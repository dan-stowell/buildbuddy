# Cap workflow integration test polling backoff at 100 ms

Workflow integration tests can wait up to 51.2 seconds between checks after exponential backoff, delaying detection of invocation creation and completion. Cap all three polling loops (workflow creation, invocation status, and cancellation child completion) at 100 ms, starting at 50 ms. Each wait uses an elapsed-time deadline and bounds its final sleep by the remaining time. Preserved the original full backoff sleep budget of 102.35 seconds (50 ms + 100 ms + ... + 51.2 s), rather than interpreting the old delay limit as a one-minute timeout. RPC time now counts toward the deadline; in-flight RPCs still use the existing context. The retryable “not found” branch also sleeps to avoid busy polling. All conditions, assertions, scenarios, and six shards remain intact.

## Premise and timing

Source inspection confirmed three exponential polling loops with delays up to 51.2 seconds. The measured saving is real in these runs; it is not a controlled estimate of upstream CI savings.

Validation used BuildBuddy Remote Bazel throughout. No local test timings were collected.

First baseline command: `bb remote test //enterprise/server/test/integration/workflow:workflow_test --runs_per_test=5 --nocache_test_results`. It passed; Bazel elapsed time was 404.785 s, including compilation. [Initial runner](https://app.buildbuddy.io/invocation/91a81aac-555e-4943-b9e8-e14f160876a3).

For the comparable before/after measurements, used the same remote script on each version: run `bazel test //enterprise/server/test/integration/workflow:workflow_test --runs_per_test=5 --nocache_test_results`, then parse every `bazel-testlogs/enterprise/server/test/integration/workflow/workflow_test/**/test.xml` with Python ElementTree. Both produced 30 XML files (six shards × five runs), containing 45 case executions.

| Metric (seconds) | Before | After |
| --- | ---: | ---: |
| Bazel elapsed time, including build | 298.036 | 294.642 |
| Bazel critical path | 96.19 | 86.35 |
| Median shard duration (sum of XML testcase times, 30 shards) | 39.160 | 28.845 |
| Sum of XML testcase durations across five runs | 1068.58 | 786.03 |
| Median slowest shard per repetition | 63.23 | 44.41 |

Median shard duration improved by 10.315 s; median slowest shard improved by 18.82 s. Runner provisioning, compilation, cache state, and shared remote load affect end-to-end times. Five repeats show variability, so these numbers do not establish a precise universal saving or eliminate flakiness risk.

[Before runner](https://app.buildbuddy.io/invocation/183a5dc5-7add-4138-862c-019e2b1ebbb9) · [Before Bazel](https://app.buildbuddy.io/invocation/40e0355e-1569-49d1-9f2c-36e08de52323)

[After runner](https://app.buildbuddy.io/invocation/3b258102-9c73-4e95-a6d9-d44a67ce9b76) · [After Bazel](https://app.buildbuddy.io/invocation/b4123f6e-0368-4d46-bbcc-f6ce1123619b)

## Before and after test-case lists and repeated outcomes

The following lists come from the test XML, not source enumeration. The before and after case sets are identical. Each row passed 5/5 before and 5/5 after; neither version had failures, errors, or skips. The initial baseline additionally passed five repetitions. Timings below are median XML testcase durations.

| Before case | After case | Before (s), passes | After (s), passes |
| --- | --- | ---: | ---: |
| TestBazelUseCLI | TestBazelUseCLI | 28.53, 5/5 | 22.58, 5/5 |
| TestCancel | TestCancel | 12.02, 5/5 | 8.21, 5/5 |
| TestCancelOlderRunsOnSameBranch | TestCancelOlderRunsOnSameBranch | 61.03, 5/5 | 34.27, 5/5 |
| TestCancelOlderRunsOnSameBranch_MultipleWorkflows | TestCancelOlderRunsOnSameBranch_MultipleWorkflows | 23.18, 5/5 | 16.97, 5/5 |
| TestExecuteAsync | TestExecuteAsync | 5.86, 5/5 | 6.95, 5/5 |
| TestExecuteWorkflow | TestExecuteWorkflow | 20.12, 5/5 | 21.11, 5/5 |
| TestInvalidYAML | TestInvalidYAML | 1.19, 5/5 | 1.55, 5/5 |
| TestTriggerLegacyWorkflowViaWebhook | TestTriggerLegacyWorkflowViaWebhook | 28.16, 5/5 | 18.78, 5/5 |
| TestTriggerViaWebhook | TestTriggerViaWebhook | 31.29, 5/5 | 25.70, 5/5 |

Both comparable invocations exited 0 and reported “Executed 1 out of 1 test: 1 test passes.” Overall changed-version result: 30/30 shard runs and 45/45 case executions passed. `gofmt` and `git diff --check` also passed.

## Scope and limitations

Did not shorten the full wait budget, remove scenarios, change assertions, change sharding, add sleeps to successful paths, or edit dependency/build/CI files. Did not add tests that mirror the loop implementation; repeated execution of the existing integration cases exercised the behavior. The PR changes only `enterprise/server/test/integration/workflow/workflow_test.go`. This description is kept locally and is not included in branch history.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3