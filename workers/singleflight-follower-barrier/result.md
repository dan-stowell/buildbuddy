# Replace dsingleflight callback sleeps with a readiness barrier

The success and error tests each held the leader callback for two seconds to let followers start waiting. Replace those sleeps with a channel barrier that releases work only after Redis `CLIENT LIST` confirms all 20 callers' result readers, including the leader's, are blocked in `XREAD`.

The worker Redis client explicitly uses `PoolSize = numWorkers + 1` (21 connections): one connection for each of the 20 blocking readers plus one for coordinator commands. This avoids the default pool limit of `10 * GOMAXPROCS`, which would prevent readiness at `GOMAXPROCS=1`. A separate client pool polls readiness. Readiness has a 10-second failure timeout, and deferred, idempotent release unblocks callbacks on failure.

The test's `BUILD` target adds the existing `@com_github_go_redis_redis_v8//:redis` dependency to construct the explicitly sized client. No production code, dependency versions, lockfiles, worker counts, or assertions changed. Both success and unavailable-error scenarios retain all coverage.

## Verification

All measurements used BuildBuddy Remote Bazel through a login shell. Baseline and final default runs used:

```sh
bash -lc 'bb remote test //enterprise/server/util/dsingleflight:dsingleflight_test --runs_per_test=5 --test_output=all'
```

The final single-CPU configuration used:

```sh
bash -lc 'bb remote test //enterprise/server/util/dsingleflight:dsingleflight_test --runs_per_test=5 --test_env=GOMAXPROCS=1 --test_output=all'
```

- [Baseline](https://app.buildbuddy.io/invocation/4760b59f-4160-40c8-b4eb-e3c67e9dbe81): **5/5 target runs passed**, 10/10 case executions passed.
- [Final default configuration](https://app.buildbuddy.io/invocation/29790b92-95a3-4eef-a024-bb276a9e5ac0): **5/5 target runs passed**, 10/10 case executions passed.
- [Final GOMAXPROCS=1 configuration](https://app.buildbuddy.io/invocation/41b73d9f-404f-47fb-9cbe-d7286cd47804): **5/5 target runs passed**, 10/10 case executions passed.

`gofmt` and `git diff --check` passed. No local fallback was needed.

## Before and after timings

The table uses Go case durations printed by Bazel, in seconds, rounded by the runner. Combined durations sum the two cases and exclude build/setup overhead. The after measurements are from the final code, including the explicit pool size.

| Run | Before TestDo | Before TestDoError | Before combined | Final default TestDo | Final default TestDoError | Final default combined | Final GOMAXPROCS=1 combined |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 2.06 | 2.02 | 4.08 | 0.04 | 0.03 | 0.07 | 0.06 |
| 2 | 2.09 | 2.02 | 4.11 | 0.04 | 0.03 | 0.07 | 0.06 |
| 3 | 2.07 | 2.03 | 4.10 | 0.03 | 0.02 | 0.05 | 0.06 |
| 4 | 2.11 | 2.03 | 4.14 | 0.03 | 0.03 | 0.06 | 0.05 |
| 5 | 2.10 | 2.03 | 4.13 | 0.03 | 0.03 | 0.06 | 0.06 |
| Mean | 2.086 | 2.026 | 4.112 | 0.034 | 0.028 | 0.062 | 0.058 |

The final default configuration saves **4.050 seconds per combined test run**. The modeled 6.4-second target duration was not reproduced by these case timings, but the roughly four-second sleep-saving premise was verified before editing and confirmed with the final implementation. Whole Bazel invocation durations are not used to attribute savings because they include build overhead and cache reuse.

## Coverage

Case lists extracted from the baseline and final Bazel `=== RUN` / `--- PASS` output:

| Before case | Final case | Baseline passes | Final default passes | Final GOMAXPROCS=1 passes |
| --- | --- | ---: | ---: | ---: |
| TestDo | TestDo | 5/5 | 5/5 | 5/5 |
| TestDoError | TestDoError | 5/5 | 5/5 | 5/5 |

The case sets are identical. All 20 workers, single-execution assertions, shared-result assertions, and unavailable-error assertions remain. No case was deleted, skipped, tagged, or weakened. Repeated runs provide flakiness evidence, not a guarantee against every future failure.

## Scope

The branch changes only `enterprise/server/util/dsingleflight/dsingleflight_test.go` and its `BUILD` file. No production seam, test parallelization, or production timing changes were needed. This report is a local, untracked artifact and is excluded from branch history. Origin's fetch URL is restored to the internal mirror. No push or PR was performed.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3