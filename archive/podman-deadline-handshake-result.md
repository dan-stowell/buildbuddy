# Podman timeout premise verification

Stopped without changing `enterprise/server/test/integration/podman/podman_test.go`, as requested when the task premise is wrong. This report is the only added file (the explicitly requested deliverable).

## Findings

At baseline commit `770b88bc89c0b20059d42a237e8c7923ff5b7215`, both `TestRun_Timeout` and `TestExec_Timeout` call `c.Run` with a five-second context deadline. `TestExec_Timeout` does not call `Create` or `Exec`: it duplicates the Run scenario. Neither timeout test asserts usage stats. Both assert DeadlineExceeded, negative exit code, exact stdout/stderr, and the output file contents.

The five-second deadlines are real, but the requested Run/Exec timeout coverage with stats assertions is absent. Changing the second test to Exec would change its scenario. Replacing deadline expiry with cancellation would change the asserted error path: commandutil distinguishes Canceled from DeadlineExceeded. I chose neither change under the coverage-preservation and stop-on-wrong-premise instructions. This does not establish that shortening the existing duplicate Run tests could never save time; the modeled eight-second saving remains unverified.

## Remote baseline and timings

Installed the BuildBuddy CLI outside the repository after discovering that neither bb nor bazel was initially available. Configured origin's public fetch URL and internal push URL as instructed. Remote Bazel started successfully; no local fallback was needed.

Command:

```sh
bash -lc 'cd ~/repo && bb remote test //enterprise/server/test/integration/podman:podman_test --config=ci --test_output=all --nocache_test_results'
```

[Remote runner](https://app.buildbuddy.io/invocation/1e116445-c437-4fa9-9082-30c93ac40909), [Bazel test invocation](https://app.buildbuddy.io/invocation/a76b9731-71dc-43ef-b4f8-80fbe931fecc).

Timing method: Go's per-case `--- PASS` durations from remote `bazel test --test_output=all`, including case cleanup. Bazel's elapsed time is reported separately.

| Measurement | Before (remote) | After |
| --- | ---: | --- |
| TestRun_Timeout | 25.25s | Not run; test unchanged |
| TestExec_Timeout | 25.45s | Not run; test unchanged |
| Bazel elapsed time | 142.469s | Not run |

Both timeout cases log a failed container shutdown approximately 15 seconds after their deadlines were created, then finish cleanup approximately 25 seconds after case start. The five-second interval is only part of their runtime. No measured improvement is claimed.

## Before/after test cases

The following list comes from the baseline Bazel test output (`=== RUN`, `--- PASS`, and `--- SKIP`), including nested cases. Before and after source sets are identical because the test file is unchanged; there was no separate after execution. All executed cases passed. The existing `TestPodmanRun_RecordsStats` skip prevents claiming every listed case passed.

| Before case | After case | Baseline outcome |
| --- | --- | --- |
| TestRunHelloWorld | TestRunHelloWorld | PASS |
| TestHelloWorldExec | TestHelloWorldExec | PASS |
| TestExecStdio | TestExecStdio | PASS |
| TestSlowRun | TestSlowRun | PASS |
| TestRun_Timeout | TestRun_Timeout | PASS |
| TestExec_Timeout | TestExec_Timeout | PASS |
| TestIsImageCached | TestIsImageCached | PASS |
| TestForceRoot | TestForceRoot | PASS |
| TestForceRoot/ForceRoot=true | TestForceRoot/ForceRoot=true | PASS |
| TestForceRoot/ForceRoot=false | TestForceRoot/ForceRoot=false | PASS |
| TestUser | TestUser | PASS |
| TestUser/username_groupname | TestUser/username_groupname | PASS |
| TestUser/uid_guid | TestUser/uid_guid | PASS |
| TestPodmanRun_LongRunningProcess_CanGetAllLogs | TestPodmanRun_LongRunningProcess_CanGetAllLogs | PASS |
| TestPodmanRun_CommandNotExecuted_RecordsStats | TestPodmanRun_CommandNotExecuted_RecordsStats | PASS |
| TestPodmanRun_RecordsStats | TestPodmanRun_RecordsStats | SKIP (pre-existing) |
| TestSignal | TestSignal | PASS |

Baseline counts: one target passed; 12 top-level cases passed, one top-level case skipped, and four nested cases passed. Each timeout case passed 1/1 executions. Repeated-run validation: not performed, because verification triggered the requested stop before implementing any change. There are no changed tests for which a five-run flakiness claim can be made.

No tests, assertions, scenarios, tags, dependencies, or CI configuration were changed. No push or PR was performed.

