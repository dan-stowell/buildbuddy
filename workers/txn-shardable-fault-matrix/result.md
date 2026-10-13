# Make raft transaction split-retry scenarios shardable

The 12 split-retry scenarios previously ran as subtests of one top-level test, so Go test sharding placed all of them on one shard. Each scenario builds a three-store cluster, leaving that shard as the target’s bottleneck. Exposing the scenarios as top-level tests lets the existing eight shards distribute the work and reduces measured uncached target wall time from **182.9s to 80.9s**.

Extracted all 12 `TestSplitTxnRetryMatrix` subtests into independent top-level tests calling `testSplitTxnRetry`. The scenario body is unchanged apart from substituting helper parameters for table fields, indentation, and adding `t.Helper()`. Each case still builds a fresh three-store cluster and uses the same fault, transaction, assertions, and cleanup. No parallel calls, sleeps, timeouts, or production code changed. `shard_count = 8` remains unchanged; `BUILD` was not edited.

## Premise and timings

Used BuildBuddy Remote Bazel throughout; no local fallback was needed. Remote runners started successfully and fetched the public fork. Both measured runs used:

```sh
bash -lc 'cd ~/repo && bb remote test //enterprise/server/raft/txn:txn_test --nocache_test_results --test_output=all --test_summary=detailed --experimental_ui_max_stdouterr_bytes=10000000'
```

Tests were uncached; build dependencies could use the remote cache. The timing method is Bazel's target `PASSED in` summary (maximum shard test-action duration). Summed top-level Go PASS durations below show workload distribution, excluding test-process overhead.

| Metric | Before | After |
| --- | ---: | ---: |
| Target test wall time | 182.9s | 80.9s |
| Bazel critical path (also includes build work) | 184.07s | 92.79s |
| Bazel invocation elapsed (also includes analysis/build) | 203.194s | 113.376s |
| Sum of top-level Go test durations across shards | 363.40s | 364.92s |

**Measured target saving: 102.0s (55.8%).** The premise holds: the original 12-case matrix alone took 182.61s and monopolized shard 5. The observed baseline differs from the survey's ~200s, and the measured saving is below its modeled upper bound of ~129s. Total test work is effectively unchanged (about 0.4% variation). The remaining longest shard includes the unchanged four-case recovery matrix; further changes are outside this task.

| Shard | Before: summed Go test time | After: summed Go test time |
| --- | ---: | ---: |
| 1 | 18.92s | 35.22s |
| 2 | 19.50s | 80.61s |
| 3 | 19.69s | 50.93s |
| 4 | 15.18s | 45.38s |
| 5 | 182.61s | 45.97s |
| 6 | 15.55s | 45.82s |
| 7 | 61.76s | 30.20s |
| 8 | 30.19s | 30.79s |

Measured baseline: https://app.buildbuddy.io/invocation/1cc23d4e-3372-4dc7-8d6f-b615fa4a6b84

Measured after: https://app.buildbuddy.io/invocation/8be94c78-2734-4fe5-86be-19b62422b7df

An initial baseline also passed (298.998s invocation elapsed, 216.75s critical path), but its longest shard log was omitted by Bazel's default 1 MiB output limit. It is excluded from the paired comparison; the rerun above raised that limit and captured all cases.

## Before/after case lists and coverage

The following lists come from Bazel's detailed test output, with the common `txn_test.` prefix removed. Every listed case passed. The 26 leaf scenarios map one-to-one; the renamed split cases keep identical `(rangeID, operation, fault timing)` values. A normalized comparison of the observed leaf-name sets confirmed equality.

| Before leaf case (PASS) | After leaf case (PASS) |
| --- | --- |
| `TestCommitPreparedTxn` | `TestCommitPreparedTxn` |
| `TestFetchTxnRecords` | `TestFetchTxnRecords` |
| `TestRecoverRollbackTxnRetryMatrix/rollback_after_apply` | `TestRecoverRollbackTxnRetryMatrix/rollback_after_apply` |
| `TestRecoverRollbackTxnRetryMatrix/rollback_before_apply` | `TestRecoverRollbackTxnRetryMatrix/rollback_before_apply` |
| `TestRecoverSplitTxnRetryMatrix/commit_meta_after_apply` | `TestRecoverSplitTxnRetryMatrix/commit_meta_after_apply` |
| `TestRecoverSplitTxnRetryMatrix/commit_meta_before_apply` | `TestRecoverSplitTxnRetryMatrix/commit_meta_before_apply` |
| `TestRecoverSplitTxnRetryMatrix/commit_right_after_apply` | `TestRecoverSplitTxnRetryMatrix/commit_right_after_apply` |
| `TestRecoverSplitTxnRetryMatrix/commit_right_before_apply` | `TestRecoverSplitTxnRetryMatrix/commit_right_before_apply` |
| `TestRecoverTxnToUpdateRangeDescriptor` | `TestRecoverTxnToUpdateRangeDescriptor` |
| `TestRollbackMarkerStampedWithFinalizeTime` | `TestRollbackMarkerStampedWithFinalizeTime` |
| `TestRollbackNotFoundPreventsLatePrepare` | `TestRollbackNotFoundPreventsLatePrepare` |
| `TestRunTxn` | `TestRunTxn` |
| `TestSplitTxnRetryMatrix/commit_left_after_apply` | `TestSplitTxnRetryMatrixCommitLeftAfterApply` |
| `TestSplitTxnRetryMatrix/commit_left_before_apply` | `TestSplitTxnRetryMatrixCommitLeftBeforeApply` |
| `TestSplitTxnRetryMatrix/commit_meta_after_apply` | `TestSplitTxnRetryMatrixCommitMetaAfterApply` |
| `TestSplitTxnRetryMatrix/commit_meta_before_apply` | `TestSplitTxnRetryMatrixCommitMetaBeforeApply` |
| `TestSplitTxnRetryMatrix/commit_right_after_apply` | `TestSplitTxnRetryMatrixCommitRightAfterApply` |
| `TestSplitTxnRetryMatrix/commit_right_before_apply` | `TestSplitTxnRetryMatrixCommitRightBeforeApply` |
| `TestSplitTxnRetryMatrix/prepare_left_after_apply` | `TestSplitTxnRetryMatrixPrepareLeftAfterApply` |
| `TestSplitTxnRetryMatrix/prepare_left_before_apply` | `TestSplitTxnRetryMatrixPrepareLeftBeforeApply` |
| `TestSplitTxnRetryMatrix/prepare_meta_after_apply` | `TestSplitTxnRetryMatrixPrepareMetaAfterApply` |
| `TestSplitTxnRetryMatrix/prepare_meta_before_apply` | `TestSplitTxnRetryMatrixPrepareMetaBeforeApply` |
| `TestSplitTxnRetryMatrix/prepare_right_after_apply` | `TestSplitTxnRetryMatrixPrepareRightAfterApply` |
| `TestSplitTxnRetryMatrix/prepare_right_before_apply` | `TestSplitTxnRetryMatrixPrepareRightBeforeApply` |
| `TestStalePendingJanitorHelpsCommit` | `TestStalePendingJanitorHelpsCommit` |
| `TestTxnRollbackRetryIsIdempotent` | `TestTxnRollbackRetryIsIdempotent` |

Bazel also reported these aggregate parent entries:

| Before parent (PASS) | After parent (PASS) |
| --- | --- |
| `TestSplitTxnRetryMatrix` | Removed grouping only; all 12 leaves appear above as top-level tests |
| `TestRecoverSplitTxnRetryMatrix` | `TestRecoverSplitTxnRetryMatrix` |
| `TestRecoverRollbackTxnRetryMatrix` | `TestRecoverRollbackTxnRetryMatrix` |

Thus the detailed summary changes from **29 passing / 0 skipped / 0 failing** to **28 passing / 0 skipped / 0 failing** solely because one grouping entry is removed. Leaf coverage remains **26/26**. Top-level test count rises from 11 to 22.

## Repeated-run validation

Ran the whole changed target with the same command plus `--runs_per_test=5` (uncached). **5/5 target runs passed; 40/40 shard executions passed; 130/130 leaf-scenario executions passed**, including **60/60 extracted split-retry executions**. Each of the 26 leaf cases in the mapping above passed **5/5** times. Bazel reported **140 passing, 0 skipped, 0 failing** entries (the 130 leaves plus the two retained aggregate parents per run). The observed detailed output contained exactly five PASS entries for each of the 28 reported names. Including the measured after run, each changed split case passed **6/6** times. No retry flags were used. This provides repeated-run evidence, not a guarantee against future flakes.

```sh
bash -lc 'cd ~/repo && bb remote test //enterprise/server/raft/txn:txn_test --nocache_test_results --test_output=all --test_summary=detailed --experimental_ui_max_stdouterr_bytes=10000000 --runs_per_test=5'
```

Repeated-run invocation: https://app.buildbuddy.io/invocation/762012d7-9d38-4d16-b8db-f0a4aae671cc

## Choices and checks

Kept eight shards: extraction alone delivers a substantial measured saving, so there is no measurement-based need to change shard count. Left both recovery matrices and all unrelated tests unchanged. Did not share cluster setup, add `t.Parallel`, shorten waits, or change assertions. A direct text comparison confirmed the extracted helper body matches the original scenario body after parameter substitution and indentation normalization. `gofmt` and `git diff --check` passed.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3