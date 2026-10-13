# Podman timeout test

Converted TestExec_Timeout into a real Exec timeout test instead of deleting it. It now creates a persistent container before starting the deadline, then calls Exec. All five original assertions remain: DeadlineExceeded, negative exit code, captured stdout, captured stderr, and readable output.txt. TestRun_Timeout continues covering Run. Existing Exec tests covered success and stdio, but no other Podman test covered Exec timeout. Only the scoped Go file changed.

## Concrete duplication proof

Before editing, extracted everything after each function's opening brace through the next top-level function. The bodies were byte-identical (1,577 bytes each), with SHA-256 c2f7e95f5ed252dc58114f9c7e8fc5a4b488cb7f100ced08ece5faba2ed99cd8. Python difflib.unified_diff returned no lines:

```diff
```

Both bodies performed identical temporary-directory/environment/provider/image setup and cleanup, pulled the image, started a five-second context deadline, called `c.Run(runCtx, cmd, workDir, oci.Credentials{})`, and executed the identical five assertions listed above. The shell command printed both streams, wrote output.txt, and slept for 100 seconds. The function names were the only difference. Extraction evidence is also in /tmp/podman-duplication.txt.

## Remote validation and timing

Identical command for both revisions, using a login shell and BuildBuddy Remote Bazel:

```sh
bb remote test //enterprise/server/test/integration/podman:podman_test --config=remote --nocache_test_results --test_output=all --runs_per_test=5
```

Both completed successfully remotely; no local fallback was needed. Baseline upload preceded the edit; the after invocation included the uncommitted edit. Fetch used the public fork and push retained the internal mirror. Origin fetch was restored afterward.

- Before: https://app.buildbuddy.io/invocation/827a7906-a0d3-497e-b09c-53437f860a39
- After: https://app.buildbuddy.io/invocation/71f37e33-c79a-4487-85f2-33b057649faf
- Local output: /tmp/podman-before.log and /tmp/podman-after.log.

Timings use Go's per-test PASS duration from all five remote runs, including test setup and cleanup, consistently before/after:

| Test | Before durations (s) | After durations (s) | Before median | After median |
| --- | --- | --- | --- | --- |
| TestExec_Timeout | 25.18, 25.20, 25.20, 25.21, 25.20 | 15.29, 15.26, 15.72, 15.41, 15.30 | 25.20s | 15.30s |
| TestRun_Timeout | 25.20, 25.24, 25.28, 25.21, 25.27 | 25.27, 25.19, 25.40, 25.25, 25.21 | 25.24s | 25.25s |

Measured median saving for the changed test: **9.90 seconds**, exceeding the five-second estimate. Bazel elapsed time for the full five-run invocation was 134.158s before and 123.446s after; these include build overhead and concurrent test runs, so the per-test timing is the stronger comparison. The deadline remains five seconds; no aggressive timeout reduction or readiness polling was introduced. Five successful repetitions provide limited flakiness evidence, not a guarantee.

## Before and after case lists

Extracted directly from Bazel's Go test output (including nested cases). The sets are identical: 13 top-level tests plus four nested cases. All runnable cases passed in every run, with zero failures; both full-target invocations passed 5/5. The existing unconditional `t.Skip()` in TestPodmanRun_RecordsStats produced five skips before and after. Therefore this report cannot claim every declared case passed; no skip was added or changed.

| Case | Before | After |
| --- | --- | --- |
| TestRunHelloWorld | PASS × 5 | PASS × 5 |
| TestHelloWorldExec | PASS × 5 | PASS × 5 |
| TestExecStdio | PASS × 5 | PASS × 5 |
| TestSlowRun | PASS × 5 | PASS × 5 |
| TestRun_Timeout | PASS × 5 | PASS × 5 |
| TestExec_Timeout | PASS × 5 | PASS × 5 |
| TestIsImageCached | PASS × 5 | PASS × 5 |
| TestForceRoot/ForceRoot=true | PASS × 5 | PASS × 5 |
| TestForceRoot/ForceRoot=false | PASS × 5 | PASS × 5 |
| TestForceRoot | PASS × 5 | PASS × 5 |
| TestUser/username_groupname | PASS × 5 | PASS × 5 |
| TestUser/uid_guid | PASS × 5 | PASS × 5 |
| TestUser | PASS × 5 | PASS × 5 |
| TestPodmanRun_LongRunningProcess_CanGetAllLogs | PASS × 5 | PASS × 5 |
| TestPodmanRun_CommandNotExecuted_RecordsStats | PASS × 5 | PASS × 5 |
| TestPodmanRun_RecordsStats | SKIP × 5 | SKIP × 5 |
| TestSignal | PASS × 5 | PASS × 5 |

## Deliberately unchanged

Did not delete the duplicate name because converting it preserves the case set and adds missing coverage. Did not modify the pre-existing skipped stats test, shorten deadlines, change assertions, or edit build/dependency configuration. Did not push or open a PR. result.md is left uncommitted.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3