# testbazel idle-server investigation

Stopped without a harness change, following the task's explicit instruction to stop if the projected saving is not real. The five-second option exists, but neither measured integration suite restarted its nested JVM between invocations. Raising the timeout would avoid **zero observed restarts** in these runs. This does not prove there are no affected tests elsewhere or on a slower runner.

## Premise and scope

`server/util/bazel/bazel.go:54` passes `--max_idle_secs=5` as a startup option. `testbazel.Invoke` delegates to it. `testbazel.BinaryPath` itself does not set this timeout; workflow, CI-runner, and remote-Bazel callers using that launcher directly are therefore not evidence of this five-second problem.

Grepping testbazel usage identified these two suites with multiple nested invocations per workspace:

- `//enterprise/server/test/integration/remote_cache:remote_cache_test`: build, clean, and rebuild in each workspace; the invalid-key case has additional clean/build scenarios. All 40 case executions started exactly one JVM each.
- `//enterprise/server/test/webdriver/invocation:invocation_test` (expanded target `invocation_test_chromium-local`): the authenticated cache-enabled case performs clean/build, browser assertions, then clean/build in the same workspace. All 15 case executions started exactly one JVM each.

I also ran `//enterprise/server/test/integration/remote_execution/bazel_rbe:bazel_rbe_test`. Its retries exercise remote actions within a single nested Bazel invocation, rather than multiple invocations per workspace, so it is supplementary coverage, not evidence of an idle-gap saving. All 75 case executions passed.

## Method and timings

All testing used **BuildBuddy Remote Bazel**, with no local fallback. These were baseline runs of unchanged source at `770b88bc89c0b20059d42a237e8c7923ff5b7215`, on 2026-10-13. Five uncached runs per target used existing sharding. Case timings below are medians of the five `--- PASS: ... (Ns)` values, including test fixture work. JVM starts were counted separately in each case's captured log using `Starting local Bazel server (9.2.0) and connecting to it...`. Every successful case had exactly one such line.

```sh
bb remote test //enterprise/server/test/integration/remote_cache:remote_cache_test //enterprise/server/test/integration/remote_execution/bazel_rbe:bazel_rbe_test --runs_per_test=5 --nocache_test_results --test_output=all
bb remote test //enterprise/server/test/webdriver/invocation:invocation_test --config=remote --runs_per_test=5 --nocache_test_results --test_output=all
```

Baseline invocations:

- [Remote cache and Bazel RBE](https://app.buildbuddy.io/invocation/4a009d6e-7779-4e6c-9a7d-66df482a0858)
- [Browser invocation tests in remote container](https://app.buildbuddy.io/invocation/320e1951-aac1-441c-ade8-7ccf07469739)

An initial browser attempt without `--config=remote` failed all 15 executions before nested Bazel because chromedriver could not load `libxcb.so.1`. [Failed baseline invocation](https://app.buildbuddy.io/invocation/ac6a9721-9597-4566-a925-149b68fe8bc2). Using the repository's remote configuration honored the test's browser-container execution properties and all 15 executions passed. No test was skipped or modified to achieve that result.

**After timings: not measured.** No candidate implementation was made after the premise check failed to find avoidable restarts. There is no measured before/after speedup, and none is claimed. The potential saving from eliminating restarts in the measured runs is zero; explicit cleanup would itself add work. I did not manufacture a six-second delay to produce a saving that the integration tests do not exhibit.

## Before and after case lists and repeated-run counts

Names below come from actual Bazel test output. “After” means the final workspace source: it is unchanged, so the before and after case sets are identical. There was no separate post-change test execution. Every listed case passed five times in the successful baseline jobs (130/130 total). Initial browser environment failures are reported above separately.

| Suite | Before case | After case | Baseline median (s) | Passes | JVM starts per run |
| --- | --- | --- | ---: | ---: | ---: |
| remote cache | `TestBuild_RemoteCacheFlags_Anonymous_SecondBuildIsCached` | `TestBuild_RemoteCacheFlags_Anonymous_SecondBuildIsCached` | 17.03 | 5/5 | 1 |
| remote cache | `TestBuild_RemoteCacheFlags_ReadWriteApiKey_SecondBuildIsCached` | `TestBuild_RemoteCacheFlags_ReadWriteApiKey_SecondBuildIsCached` | 14.14 | 5/5 | 1 |
| remote cache | `TestBuild_RemoteCacheFlags_ReadOnlyApiKey_SecondBuildIsNotCached` | `TestBuild_RemoteCacheFlags_ReadOnlyApiKey_SecondBuildIsNotCached` | 14.05 | 5/5 | 1 |
| remote cache | `TestBuild_RemoteCacheFlags_CasOnlyApiKey_SecondBuildIsNotCached` | `TestBuild_RemoteCacheFlags_CasOnlyApiKey_SecondBuildIsNotCached` | 15.83 | 5/5 | 1 |
| remote cache | `TestBuild_RemoteCacheFlags_NoAuthConfigured_SecondBuildIsCached` | `TestBuild_RemoteCacheFlags_NoAuthConfigured_SecondBuildIsCached` | 18.92 | 5/5 | 1 |
| remote cache | `TestBuild_RemoteCacheFlags_Compression_SecondBuildIsCached` | `TestBuild_RemoteCacheFlags_Compression_SecondBuildIsCached` | 18.45 | 5/5 | 1 |
| remote cache | `TestBuild_RemoteCache_ScoreCard` | `TestBuild_RemoteCache_ScoreCard` | 17.92 | 5/5 | 1 |
| remote cache | `TestBuild_RemoteCache_RejectsInvalidAPIKeys` | `TestBuild_RemoteCache_RejectsInvalidAPIKeys` | 19.76 | 5/5 | 1 |
| Bazel RBE | `TestSimpleAction_Exit0` | `TestSimpleAction_Exit0` | 14.61 | 5/5 | 1 |
| Bazel RBE | `TestSimpleAction_Exit1` | `TestSimpleAction_Exit1` | 15.58 | 5/5 | 1 |
| Bazel RBE | `TestSimpleAction_TerminateWithSIGKILL` | `TestSimpleAction_TerminateWithSIGKILL` | 14.44 | 5/5 | 1 |
| Bazel RBE | `TestSimpleAction_TerminateWithSIGABRT` | `TestSimpleAction_TerminateWithSIGABRT` | 14.07 | 5/5 | 1 |
| Bazel RBE | `TestPersistentUnavailableError_Retried` | `TestPersistentUnavailableError_Retried` | 14.01 | 5/5 | 1 |
| Bazel RBE | `TestTransientUnavailableError_Retried` | `TestTransientUnavailableError_Retried` | 14.09 | 5/5 | 1 |
| Bazel RBE | `TestTransientInternalError_Retried` | `TestTransientInternalError_Retried` | 14.68 | 5/5 | 1 |
| Bazel RBE | `TestTransientAbortedError_Retried` | `TestTransientAbortedError_Retried` | 13.81 | 5/5 | 1 |
| Bazel RBE | `TestDeadlineExceededError_NotRetried` | `TestDeadlineExceededError_NotRetried` | 13.70 | 5/5 | 1 |
| Bazel RBE | `TestUnauthenticatedError_RetriedOnce` | `TestUnauthenticatedError_RetriedOnce` | 14.09 | 5/5 | 1 |
| Bazel RBE | `TestTransientExecutorShutdown_Retried` | `TestTransientExecutorShutdown_Retried` | 13.68 | 5/5 | 1 |
| Bazel RBE | `TestPersistentExecutorShutdown_Retried` | `TestPersistentExecutorShutdown_Retried` | 14.33 | 5/5 | 1 |
| Bazel RBE | `TestTransientCacheNotFoundError_Retried` | `TestTransientCacheNotFoundError_Retried` | 13.77 | 5/5 | 1 |
| Bazel RBE | `TestActionWithContainerImage_InvalidArgument` | `TestActionWithContainerImage_InvalidArgument` | 13.89 | 5/5 | 1 |
| Bazel RBE | `TestActionWithRunnerRecycling_InvalidArgument` | `TestActionWithRunnerRecycling_InvalidArgument` | 10.97 | 5/5 | 1 |
| browser invocation | `TestInvocationWithRemoteExecution` | `TestInvocationWithRemoteExecution` | 19.02 | 5/5 | 1 |
| browser invocation | `TestAuthenticatedInvocation_PersonalAPIKey_CacheEnabled` | `TestAuthenticatedInvocation_PersonalAPIKey_CacheEnabled` | 20.75 | 5/5 | 1 |
| browser invocation | `TestAuthenticatedInvocation_CacheEnabled` | `TestAuthenticatedInvocation_CacheEnabled` | 21.08 | 5/5 | 1 |

## Final state and choices

No source files, assertions, scenarios, tags, dependencies, or fixtures changed. No sharing was introduced: existing workspace and service isolation remains intact. No longer-lived JVMs were introduced and no new cleanup behavior is needed. I chose not to add a launcher override or broaden the shared utility API without evidence of a saving, consistent with the shared-harness caution and explicit stop condition.

`result.md` is the only created workspace file and is intentionally not committed. There is no implementation commit because the investigation stopped without source changes; no push or PR was performed. The origin fetch URL was restored to the internal mirror, with the push URL also on that mirror.

Captured full logs remain at `/tmp/testbazel-before.log`, `/tmp/testbazel-web-before.log`, and `/tmp/testbazel-web-remote-before.log` on this VM.

