# Compact CLI integration test parallelization

Changed only `cli/test/integration/compact/compact_test.go`, plus this explicitly requested report. Fixture/sort subtests and their three repetitions now use `t.Parallel()`. Each repetition creates its own workspace. Input logs and expected JSON remain shared read-only fixtures. The parent initializes `testcli.Command` without executing a command, keeping the helper's shared temporary HOME alive until all parallel children finish. Both existing assertions are unchanged.

## Premise and timing

The source confirmed 12 independent serial CLI executions (2 fixtures × 2 sort modes × 3 repetitions). The modeled 18 seconds and ~9-second saving did not reproduce on this remote runner. A real smaller saving did reproduce, so the change was retained.

Used BuildBuddy Remote Bazel for all tests; no local fallback. Installed the missing CLI using the documented installer at https://install.buildbuddy.io. Configured origin to fetch the public fork and push to the existing mirror as requested. No pushes or PRs.

The matching timing command for both versions was:

```sh
bash -lc 'cd ~/repo && bb remote test //cli/test/integration/compact:compact_test --runs_per_test=5 --nocache_test_results --local_test_jobs=1 --test_output=all --test_summary=detailed --test_arg=-test.v'
```

Measured Bazel test-action wall times from its detailed summary (includes parallel children and workspace setup; excludes build/analysis and runner provisioning). `--local_test_jobs=1` prevents contention between the five test actions; Go still schedules parallel subtests normally.

| Version | Runs | Min | Max | Mean | Std. dev. |
| --- | --- | --- | --- | --- | --- |
| Before | 5 | 2.1s | 2.5s | 2.2s | 0.1s |
| After | 5 | 0.9s | 1.5s | 1.1s | 0.2s |

Mean saving: approximately **1.1s / 50%**, using Bazel's rounded values. Overall Bazel invocations took 45.078s before and 39.536s after, but these include setup/build differences and are not the claimed saving. Go's parent-test duration is not suitable for timing parallel children.

Before invocation: https://app.buildbuddy.io/invocation/df656018-189c-4ffe-abaf-f11e75528b4f

After invocation: https://app.buildbuddy.io/invocation/d79226f7-fe80-43fa-8f5f-be13662f6208

## Coverage and repeated runs

Both timed invocations reported **85 passing, 0 skipped, 0 failing**: the same 17 names (parent, four fixture/sort groups, and 12 repetitions), each passing five times. The lists below were extracted from verbose Bazel test output and compared programmatically for exact set equality; every name had five PASS records in each version.

An additional initial five-run invocation also passed for each version, giving **10/10 runs and 120/120 leaf repetition executions passing per version**. Initial runs allowed concurrent Bazel test actions and were excluded from the controlled timing comparison.

Initial before: https://app.buildbuddy.io/invocation/05c5568f-0489-499d-99b5-a1f4f7571f1a

Initial after: https://app.buildbuddy.io/invocation/4810ab7b-d67a-4f18-be94-f4847347dbcf

### Before test-case list

```text
TestPrintCompactExec
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_false
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_false/run_0
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_false/run_1
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_false/run_2
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_true
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_true/run_0
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_true/run_1
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_true/run_2
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_false
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_false/run_0
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_false/run_1
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_false/run_2
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_true
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_true/run_0
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_true/run_1
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_true/run_2
```

### After test-case list

```text
TestPrintCompactExec
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_false
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_false/run_0
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_false/run_1
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_false/run_2
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_true
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_true/run_0
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_true/run_1
TestPrintCompactExec/testdata/1.binpb.zst/isSorted_true/run_2
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_false
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_false/run_0
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_false/run_1
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_false/run_2
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_true
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_true/run_0
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_true/run_1
TestPrintCompactExec/testdata/2.binpb.zst/isSorted_true/run_2
```

## Limits and choices

No cases, repetitions, sort modes, assertions, timeouts, BUILD files, dependencies, or CI configuration were removed or weakened. No broader test suite was run because the change is confined to this test's scheduling and workspace ownership. Ten successful runs provide flakiness evidence, not a guarantee. The remote timings do not establish a specific saving on the two-CPU local VM or upstream CI.


---
Part of a coordinated effort to speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3