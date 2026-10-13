# Split remote Bazel runfiles scenarios and cap readiness polling

Changed only `enterprise/server/test/integration/remote_bazel/BUILD` and `remote_bazel_test.go`. The three independent runfiles scenarios now have separate top-level tests backed by the same helper, and shard_count increases from 7 to 13. All three remote builds, generated files, CLI flags, runfiles paths, expected outputs, and assertions remain covered. Each scenario gets an isolated server/executor rather than sharing one across serial builds.

The three readiness helpers (`waitForInvocationCreated`, `waitForLogLine`, and `waitForInvocationStatus`) now start at 50 ms and cap exponential backoff at 100 ms. An elapsed-time deadline replaces the delay-based loop limit; the final sleep is clipped to the remaining budget. The timeout budget stays 102.35 seconds, matching the old sum of sleeps from 50 ms through 51.2 seconds. Each helper derives its RPC context from the overall deadline with `context.WithDeadline(ctx, deadline)` and defers cancellation, so a stalled RPC is bounded by the same budget. Earlier caller deadlines and cancellation remain effective. Assertions are unchanged. The elapsed-time budget includes RPC time, whereas the old loop added RPC time on top of its sleep budget.

## Bottleneck and shard assignment

Before, one-based shard 6 contained only `TestBuildRemotelyRunLocally_Runfiles`, which serially ran three subtests. Baseline XML showed these durations:

| Scenario | Run 1 (s) | Run 2 (s) | Run 3 (s) | Run 4 (s) | Run 5 (s) |
|---|---:|---:|---:|---:|---:|
| resolves_apparent_Bzlmod_repo_name | 29.17 | 30.23 | 30.83 | 29.17 | 18.45 |
| runs_even_if_RUNFILES_DIR_is_unset | 29.62 | 31.70 | 31.87 | 31.00 | 38.85 |
| runs_with_runfiles | 27.57 | 33.15 | 33.37 | 30.48 | 32.16 |

The baseline shard-6 run-1 test.log reports three nested Bazel invocations with elapsed times 19.168, 19.702, and 22.609 seconds, but action critical paths of only 0.46, 0.36, and 0.69 seconds. Each scenario creates a fresh repo and runs the remote CLI in a separate process, so Bazel startup/analysis and runner lifecycle dominate these small builds. Serial execution accumulates this cost. The polling helpers are not used by these cases.

The pinned rules_go 0.63.0 [test main generator](https://github.com/bazel-contrib/rules_go/blob/v0.63.0/go/tools/builders/generate_test_main.go) collects tests in declaration order and assigns index i to zero-based shard `i % TEST_TOTAL_SHARDS`. Subtests stay with their parent. There are now 13 top-level tests: 13 shards assign one to each without empty shards. The three runfiles scenarios occupy one-based shards 6, 7, and 8. Merely increasing shard_count without splitting the parent would not fix the bottleneck.

## Remote validation and timings

Both measurements used BuildBuddy Remote Bazel on the same base checkout `770b88bc89c0b20059d42a237e8c7923ff5b7215`, with uncommitted test/BUILD changes included in the after invocation. No local Bazel fallback was used. For remote validation, origin fetch was set to `https://github.com/dan-stowell/buildbuddy.git`, and push remained `https://github.int.exe.xyz/dan-stowell/buildbuddy.git`. After validation, fetch was restored to the mirror for PR tooling. Commands ran in a bash login shell so BUILDBUDDY_API_KEY was available:

```sh
bb remote test //enterprise/server/test/integration/remote_bazel:remote_bazel_test --runs_per_test=5 --test_output=errors
```

- [Before Bazel invocation](https://app.buildbuddy.io/invocation/e0dc7cb0-a0f1-4c79-9e3e-5aaed331f2e4), [runner](https://app.buildbuddy.io/invocation/6109b3af-aa17-4204-a600-6e9db373e6e2).
- [After Bazel invocation](https://app.buildbuddy.io/invocation/70c37a9e-ecc1-435a-b055-1d500a44f137), [runner](https://app.buildbuddy.io/invocation/85e44e61-d89e-4ed1-8094-28033b952460).

The same timing method was used before and after: retrieve test.xml via BuildBuddy GetAction/GetFile, sum top-level testsuite time per shard, and take the longest shard for each repetition. Nested testcase times are not counted again. Shards below use the API's one-based numbering.

| Repetition | Before longest shard (s) | After longest shard (s) | Reduction (s) |
|---|---:|---:|---:|
| 1 | 88.260 | 23.610 | 64.650 |
| 2 | 100.275 | 23.080 | 77.195 |
| 3 | 98.560 | 22.291 | 76.269 |
| 4 | 92.251 | 22.260 | 69.991 |
| 5 | 90.850 | 22.524 | 68.326 |

Median longest shard: **92.251 → 22.524 seconds**, a 69.727-second (75.6%) reduction.

The complete final after shard measurements:

| Shard | Run 1 (s) | Run 2 (s) | Run 3 (s) | Run 4 (s) | Run 5 (s) |
|---|---:|---:|---:|---:|---:|
| 1 | 19.060 | 22.190 | 22.193 | 18.930 | 20.590 |
| 2 | 12.085 | 11.390 | 11.145 | 11.104 | 10.920 |
| 3 | 6.971 | 7.110 | 17.200 | 18.305 | 15.241 |
| 4 | 15.705 | 16.310 | 16.090 | 13.605 | 19.360 |
| 5 | 19.725 | 22.994 | 20.251 | 20.623 | 16.334 |
| 6 | 21.684 | 17.644 | 16.730 | 22.260 | 21.895 |
| 7 | 19.240 | 18.980 | 21.800 | 18.770 | 22.524 |
| 8 | 21.963 | 16.940 | 19.820 | 18.340 | 19.450 |
| 9 | 23.610 | 23.080 | 22.291 | 20.643 | 20.540 |
| 10 | 15.570 | 9.850 | 9.440 | 10.173 | 9.224 |
| 11 | 6.990 | 7.500 | 8.060 | 7.903 | 8.663 |
| 12 | 5.090 | 5.150 | 2.764 | 1.910 | 3.664 |
| 13 | 9.750 | 14.660 | 15.500 | 16.602 | 14.340 |

Whole Bazel invocation elapsed time was 537.352 → 286.670 seconds; reported invocation critical path was 213.46 → 50.20 seconds. These include compilation and scheduling and are not controlled test-only measurements. The shard comparison supports a shorter package critical path; the saving in the entire upstream CI pipeline depends on competing targets and available concurrency. Separate hosted invocations can vary in load/cache state. The split also adds two server/executor setups and six shard processes per repetition, increasing overhead while shortening the longest serial workload.

## Coverage and five-repeat results

Before raw XML list (14 entries):

```text
TestAccessingSecrets
TestBBRC
TestBashScript
TestBuildRemotelyRunLocally
TestBuildRemotelyRunLocally_ExecutableRunfile
TestBuildRemotelyRunLocally_Runfiles
TestBuildRemotelyRunLocally_Runfiles/resolves_apparent_Bzlmod_repo_name
TestBuildRemotelyRunLocally_Runfiles/runs_even_if_RUNFILES_DIR_is_unset
TestBuildRemotelyRunLocally_Runfiles/runs_with_runfiles
TestBuildRemotelyRunLocally_ShBinary
TestCancel
TestEmbeddedBinariesFromApp
TestFetchRemoteBuildOutputs
TestWithPrivateRepo
```

After raw XML list (16 entries):

```text
TestAccessingSecrets
TestBBRC
TestBashScript
TestBuildRemotelyRunLocally
TestBuildRemotelyRunLocally_ExecutableRunfile
TestBuildRemotelyRunLocally_Runfiles
TestBuildRemotelyRunLocally_Runfiles/runs_with_runfiles
TestBuildRemotelyRunLocally_Runfiles_BzlmodRepoName
TestBuildRemotelyRunLocally_Runfiles_BzlmodRepoName/resolves_apparent_Bzlmod_repo_name
TestBuildRemotelyRunLocally_Runfiles_UnsetEnv
TestBuildRemotelyRunLocally_Runfiles_UnsetEnv/runs_even_if_RUNFILES_DIR_is_unset
TestBuildRemotelyRunLocally_ShBinary
TestCancel
TestEmbeddedBinariesFromApp
TestFetchRemoteBuildOutputs
TestWithPrivateRepo
```

Two subtest paths changed solely because their scenarios moved to new top-level parents:

| Before | After |
|---|---|
| `TestBuildRemotelyRunLocally_Runfiles/runs_even_if_RUNFILES_DIR_is_unset` | `TestBuildRemotelyRunLocally_Runfiles_UnsetEnv/runs_even_if_RUNFILES_DIR_is_unset` |
| `TestBuildRemotelyRunLocally_Runfiles/resolves_apparent_Bzlmod_repo_name` | `TestBuildRemotelyRunLocally_Runfiles_BzlmodRepoName/resolves_apparent_Bzlmod_repo_name` |

All other names remain unchanged. Normalizing these two path relocations, the after set is a **superset** of the before set; the two added entries are the new top-level parents. This comparison was checked programmatically against the XML. Every original scenario is still present, with the same data and assertion, and no assertion, test, or scenario was deleted, skipped, manual-tagged, or weakened.

Every before XML entry passed **5/5**: 70 passing records across 35 shard executions. Every after XML entry passed **5/5**: 80 passing records across 65 shard executions. In particular, all three changed runfiles scenarios and TestCancel passed five times each in the final combined-change run. Both remote commands exited 0 and reported the target passing. Zero failures, errors, or skips occurred in the XML. `gofmt` and `git diff --check` also passed.


The intermediate split-only remote run also passed every case 5/5 ([invocation](https://app.buildbuddy.io/invocation/44e1df63-10ee-45e8-82e4-4c8d72263abe)), with a 31.710-second median longest shard. The previous split-plus-cap run passed every case 5/5 with a 34.083-second median longest shard ([invocation](https://app.buildbuddy.io/invocation/cb895cc8-a8a4-4261-939e-e3eccc0414d4)). The final run above includes the RPC deadline fix as well; its raw after test-case list is identical to that previous run.

| TestCancel timing | Run 1 (s) | Run 2 (s) | Run 3 (s) | Run 4 (s) | Run 5 (s) | Median (s) |
|---|---:|---:|---:|---:|---:|---:|
| Original | 13.14 | 11.40 | 11.91 | 11.12 | 11.13 | 11.40 |
| Split only | 12.71 | 11.91 | 11.32 | 11.60 | 11.57 | 11.60 |
| Split + polling cap | 13.12 | 12.04 | 10.98 | 10.98 | 11.70 | 11.70 |
| Final, including RPC deadlines | 12.08 | 11.39 | 11.14 | 11.10 | 10.92 | 11.14 |

The small differences in TestCancel timings do not establish a separate speedup from the polling/context changes. Runner load and cache state also differ between invocations; the latest invocation used fewer build actions than the prior run, so its lower package times should not be attributed to the RPC deadline fix. The cap prevents readiness detection from waiting through a potentially 51.2-second sleep, while preserving the overall sleep-budget timeout. Increasing polling frequency can add RPC load; all five repetitions passed, but this is not a guarantee against future flakes.

## Deliberately not changed

No t.Parallel was introduced: server setup changes process-global flags, while Bazel shards provide process isolation. No timeout reduction, dependency/lockfile changes, CI edits, push, or PR.

`result.md` remains local and untracked as requested; it was removed from the previous commit with `git rm --cached result.md` and the commit amended. Only the in-scope BUILD and test file changes are committed. All branch commits were inspected for any path with basename result.md; none contains one. The empty report-only commit left by the earlier amendment was also dropped. `git diff --stat origin/master...HEAD` lists only the package BUILD and Go test file.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3