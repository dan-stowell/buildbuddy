# Shared test server readiness polling

Changed only `server/testutil/testserver/testserver.go`: 500 ms → 50 ms. The 60 s timeout, `/readyz?server-type=...` request, OK-body check, process monitoring and cleanup are unchanged. No test/scenario/assertion changes.

## Premise and server starts

Confirmed the original 500 ms sleep in the readiness loop. The claim that every integration server goes through it is too broad: `rbetest.AddBuildBuddyServerWithOptions` creates an in-process server and bypasses this helper. App binaries started by `buildbuddy.Run` / `buildbuddy_enterprise.RunWithConfig` and executor binaries started by `testexecutor.Run` do use it.

Static survey of both main integration trees (direct app-start call sites, not runtime counts; helpers/subtests can multiply these):

| Target directory | Direct start sites |
|---|---:|
| `enterprise/server/test/integration/build_logs` | 2 |
| `enterprise/server/test/integration/ci_runner` | 40 |
| `enterprise/server/test/integration/cli/stream_run_logs` | 1 |
| `enterprise/server/test/integration/invocation_webhook` | 1 |
| `enterprise/server/test/integration/remote_cache` | 8 |
| `enterprise/server/test/integration/remote_execution` | 1 |
| `enterprise/server/test/integration/usage` | 1 |
| `server/test/integration/build_event_protocol` | 3 |
| `server/test/integration/remote_asset` | 1 |

Total: 58 direct app-start sites. This is not an exact full-suite execution count. For the measured full targets the actual counts per suite run are build_event_protocol=6, remote_asset=4, remote_cache=8, build_logs=2: 20 starts/run, 100 starts across five runs on each side. Before logs contain 100 health-ready transitions; after logs contain 99 (one build-log transition was not captured), with all 100 cases completing successfully.

## Remote measurements

Both sides used BuildBuddy Remote Bazel, identical targets and flags, existing sharding, no filtering, and no cached test results:

```bash
bb remote test //server/test/integration/remote_asset:remote_asset_test \
  //server/test/integration/build_event_protocol:build_event_protocol_test \
  //enterprise/server/test/integration/remote_cache:remote_cache_test \
  //enterprise/server/test/integration/build_logs:build_logs_test \
  --nocache_test_results --runs_per_test=5 --test_output=all --test_arg=-test.v
```

Timing method: use Go verbose PASS case wall durations, sum cases within each shard, then take the maximum shard per target/run. This measures test-body wall time on the longest shard; it excludes test-process startup overhead and is not the scheduler makespan. Same extraction on both sides. Unsharded build_logs sums its two cases.

| Target | Before runs (s) | After runs (s) | Mean before → after (s) |
|---|---|---|---|
| `//server/test/integration/build_event_protocol:build_event_protocol_test` | 13.82, 14.44, 14.11, 13.78, 14.23 | 12.28, 10.43, 12.81, 12.22, 12.64 | 14.076 → 12.076 |
| `//server/test/integration/remote_asset:remote_asset_test` | 14.00, 13.80, 13.74, 13.65, 13.23 | 9.26, 9.99, 11.47, 10.79, 11.25 | 13.684 → 10.552 |
| `//enterprise/server/test/integration/build_logs:build_logs_test` | 4.59, 4.60, 4.59, 3.62, 2.38 | 4.33, 3.37, 4.37, 4.03, 4.26 | 3.956 → 4.072 |
| `//enterprise/server/test/integration/remote_cache:remote_cache_test` | 18.10, 18.58, 18.70, 19.80, 18.33 | 11.98, 11.63, 12.21, 12.64, 12.36 | 18.702 → 12.164 |

Remote Bazel total invocation elapsed time: 348.505 s before, 237.397 s after (includes analysis/build/tests). These large reductions cannot be attributed to readiness polling alone: compilation/cache state, nested Bazel builds and runner load differ. Build_logs total duration did not improve (3.956 → 4.072 s).

A more focused startup metric uses server log timestamps from `HealthChecker transitioning ... ready: true` to `Opened invocation channel` in build_logs, which sends events directly after startup. This includes client/event setup, so it is a proxy for readiness-detection overhead, not an exact measurement of waitForReady.

| Metric | Before | After |
|---|---:|---:|
| Samples | 10 | 9 |
| Mean delay | 276.9 ms | 37.1 ms |

Observed reduction: 239.8 ms/start in this proxy metric, consistent with the premise. One after sample lacks the ready-transition log and is excluded from this metric only, not from test execution. The uniform-phase mathematical expectation for 500 → 50 ms is 225 ms/start; real startup phases need not be uniform. This supports retaining the minimal change, without claiming all target wall-time improvements are causal.

Before invocation: https://app.buildbuddy.io/invocation/4f9b54b9-19b0-4121-b411-c0e1457d6a38
After invocation: https://app.buildbuddy.io/invocation/72ce4504-b61d-477b-ad77-14753773e97e
Raw verbose output: `/tmp/readiness-before.log`, `/tmp/readiness-after.log`.

## Coverage and repeatability

Lists below are extracted from verbose Bazel test output (PASS names), sorted. All four full targets passed; every listed case passed 5/5 before and 5/5 after. Each side: 20 distinct cases, 100 passes, 0 failures, 0 skips reported. Identical sets and occurrence counts verified programmatically. Five repeats are evidence of stability, not a guarantee against flakes.

Before:

```
TestBuildLogs_CompletedInvocation
TestBuildLogs_InProgressInvocation
TestBuildWithBESFlags_Success
TestBuildWithRetry_InjectFailureAfterBuildFinished
TestBuildWithRetry_InjectFailureAfterBuildStarted
TestBuildWithRetry_InjectFailureAfterProgress
TestBuildWithRetry_InjectFailureAfterWorkspaceStatus
TestBuildWithRetry_InjectFailureWhileServerIsSendingACKs
TestBuild_RemoteCacheFlags_Anonymous_SecondBuildIsCached
TestBuild_RemoteCacheFlags_CasOnlyApiKey_SecondBuildIsNotCached
TestBuild_RemoteCacheFlags_Compression_SecondBuildIsCached
TestBuild_RemoteCacheFlags_NoAuthConfigured_SecondBuildIsCached
TestBuild_RemoteCacheFlags_ReadOnlyApiKey_SecondBuildIsNotCached
TestBuild_RemoteCacheFlags_ReadWriteApiKey_SecondBuildIsCached
TestBuild_RemoteCache_RejectsInvalidAPIKeys
TestBuild_RemoteCache_ScoreCard
TestRemoteAsset_FallbackToSecondURLAfterNotFound_FetchesSuccessfully
TestRemoteAsset_FallbackToSecondURLAfterUnreachable_FetchesSuccessfully
TestRemoteAsset_OKResponse_FetchesSuccessfully
TestRemoteAsset_WithProxy
```

After:

```
TestBuildLogs_CompletedInvocation
TestBuildLogs_InProgressInvocation
TestBuildWithBESFlags_Success
TestBuildWithRetry_InjectFailureAfterBuildFinished
TestBuildWithRetry_InjectFailureAfterBuildStarted
TestBuildWithRetry_InjectFailureAfterProgress
TestBuildWithRetry_InjectFailureAfterWorkspaceStatus
TestBuildWithRetry_InjectFailureWhileServerIsSendingACKs
TestBuild_RemoteCacheFlags_Anonymous_SecondBuildIsCached
TestBuild_RemoteCacheFlags_CasOnlyApiKey_SecondBuildIsNotCached
TestBuild_RemoteCacheFlags_Compression_SecondBuildIsCached
TestBuild_RemoteCacheFlags_NoAuthConfigured_SecondBuildIsCached
TestBuild_RemoteCacheFlags_ReadOnlyApiKey_SecondBuildIsNotCached
TestBuild_RemoteCacheFlags_ReadWriteApiKey_SecondBuildIsCached
TestBuild_RemoteCache_RejectsInvalidAPIKeys
TestBuild_RemoteCache_ScoreCard
TestRemoteAsset_FallbackToSecondURLAfterNotFound_FetchesSuccessfully
TestRemoteAsset_FallbackToSecondURLAfterUnreachable_FetchesSuccessfully
TestRemoteAsset_OKResponse_FetchesSuccessfully
TestRemoteAsset_WithProxy
```

## Choices and limits

Used 50 ms rather than 25 ms to limit added health requests. No new tests, timeouts, sharding changes, or unrelated optimization. Did not run the entire integration suite, Docker/KVM targets, or local Bazel; remote execution worked. Full-suite starts are a static call-site survey rather than an instrumented runtime census. `git diff --check` passed. `result.md` is intentionally not committed.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3