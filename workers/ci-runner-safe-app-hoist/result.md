# CI runner fixture hoisting

Changed only `enterprise/server/test/integration/ci_runner/ci_runner_test.go`: start the OSS BES app once per parent in the two requested table tests. Move the Bash-options workspace into its child. No cases, assertions, scenarios, timeouts or tags changed.

## Premise and isolation

Before: two children in each parent, each calling `buildbuddy.Run(t)` (four app startups). After: one app per parent (two startups), saving two startups per execution of the selected tests. Neither test exercises app startup or migration.

Each child has its own temporary workspace and unique git repo (the helper adds a random `.repo_id`), with separate runner processes. Commit-SHA cases intentionally reuse their own workspace across their three runner invocations to preserve the recycling/checkout coverage. Runner invocation IDs are generated independently; lookups use the current runner's exact ID. The explicit all-zero inner Bazel invocation ID is preserved and used by only one child per app lifetime. These are anonymous OSS BES tests, with no API keys/groups to separate. The parent owns the shared app's DB/storage/cache and cleanup; children do not mutate app configuration or query other invocations. A child assertion failure does not stop or clean up the parent app, and no child relies on another child's repo or workspace leftovers.

## Measurement and validation

Both baseline and changed runs used BuildBuddy Remote Bazel successfully; no local fallback was needed. Exact command (same for both):

```sh
bash -lc 'cd ~/repo && bb remote test //enterprise/server/test/integration/ci_runner:ci_runner_test --test_filter="TestCIRunner_RunsBashCommands_BazelWithOptions|TestRunAction_RespectsCommitSha" --test_arg=-test.v --runs_per_test=5 --nocache_test_results --test_output=all'
```

Timings use the parent Go test PASS durations from Bazel output, including fixture setup and cleanup, excluding remote queuing/build/analysis. Five repetitions per phase. Existing 31-way sharding was retained; empty filtered shards are not counted as case passes.

| Parent | Before seconds (all runs) | After seconds (all runs) | Mean before → after | Mean saving |
|---|---|---|---|---|
| TestCIRunner_RunsBashCommands_BazelWithOptions | 26.59, 25.89, 25.99, 26.03, 26.02 | 20.21, 20.91, 22.15, 22.54, 23.27 | 26.10 → 21.82 | 4.29s |
| TestRunAction_RespectsCommitSha | 43.45, 46.45, 46.51, 46.87, 46.79 | 43.49, 43.66, 43.91, 43.97, 44.14 | 46.01 → 43.83 | 2.18s |

Combined mean selected-test duration: 72.12s → 65.65s, saving 6.47s (~9%). Remote scheduling/load and nested Bazel work add noise; this is an observed comparison, not a guarantee that all of the difference comes from two startups.

## Before/after case lists

Lists below are extracted from actual Bazel verbose test PASS output (parents and nested children). They are identical.

### Before

- `TestCIRunner_RunsBashCommands_BazelWithOptions` — 5/5 passes
- `TestCIRunner_RunsBashCommands_BazelWithOptions/Has_additional_bazel_args` — 5/5 passes
- `TestCIRunner_RunsBashCommands_BazelWithOptions/Has_startup_options` — 5/5 passes
- `TestRunAction_RespectsCommitSha` — 5/5 passes
- `TestRunAction_RespectsCommitSha/setBranchName_false` — 5/5 passes
- `TestRunAction_RespectsCommitSha/setBranchName_true` — 5/5 passes

### After

- `TestCIRunner_RunsBashCommands_BazelWithOptions` — 5/5 passes
- `TestCIRunner_RunsBashCommands_BazelWithOptions/Has_additional_bazel_args` — 5/5 passes
- `TestCIRunner_RunsBashCommands_BazelWithOptions/Has_startup_options` — 5/5 passes
- `TestRunAction_RespectsCommitSha` — 5/5 passes
- `TestRunAction_RespectsCommitSha/setBranchName_false` — 5/5 passes
- `TestRunAction_RespectsCommitSha/setBranchName_true` — 5/5 passes

Both Bazel invocations completed successfully: `Executed 1 out of 1 test: 1 test passes.` All four child cases passed five times before and five times after (20 child passes per phase); no failures observed. This sample does not establish the absence of rare flakes.

Raw output: `/tmp/ci_runner_before.log` and `/tmp/ci_runner_after.log`.

## Scope and exclusions

Did not change any other test or fixture helper, introduce parallelism, or run the full unfiltered integration target. Only the two affected parents were tested. `git diff --check` passed. This report is left uncommitted; only the scoped Go file is committed. Origin fetch URL restored to the internal mirror; nothing pushed and no PR opened.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3