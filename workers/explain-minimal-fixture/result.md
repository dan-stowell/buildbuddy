# Explain fixture-log test setup

Changed only cli/test/integration/explain/explain_test.go. Each case now uses its own t.TempDir containing copies of the two recorded execution logs. Removed NewWorkspace calls (Bazel launcher/module/lockfile setup plus Git initialization, configuration, and initial commit). BUILD dependencies are unchanged.

Premise verified: HandleExplain bypasses workspace invocation history when --old and --new are supplied. Both JSON and protobuf commands passed from directories containing only the logs, with no Bazel workspace or Git repository. NewWorkspace does not actually run a Bazel build; the projected saving should be understood as removing setup, not two builds.

All existing command success, clean output decoding, spawn count, mnemonic, modification, nonempty diff, and target label assertions remain unchanged. No cases were deleted, skipped, tagged, or weakened. Each case creates separate copies and a separate working directory; neither depends on another case's files. Existing testcli command environment handling is unchanged. No fixture is shared.

## Measurement

All measurements used BuildBuddy Remote Bazel (no local fallback), with identical commands before and after:

```sh
bash -lc 'bb remote test //cli/test/integration/explain:explain_test --runs_per_test=5 --nocache_test_results --test_output=all'
```

Two alternating before/after pairs were run. Case timings come from the verbose Bazel Go test output; each sample sums the JSON and protobuf case durations within one test execution. Values are rounded by the Go test runner. Bazel elapsed time includes analysis/build/cache effects and is not an isolated test measurement; remote runner waiting is excluded.

| Version/batch | Summed case durations for five runs (seconds) | Mean (seconds) | Bazel elapsed (seconds) |
| --- | --- | --- | --- |
| before | 3.78, 3.59, 2.65, 1.42, 0.55 | 2.398 | 45.314 |
| after | 0.85, 0.84, 0.75, 0.81, 0.57 | 0.764 | 36.709 |
| before2 | 3.57, 3.26, 3.05, 2.72, 2.79 | 3.078 | 30.671 |
| after2 | 0.80, 0.57, 0.62, 0.64, 0.62 | 0.650 | 28.508 |

Across ten executions per version, mean summed case time fell from 2.738s to 0.707s (about 74%, 2.031s per execution). First-case timing varies substantially and includes one-time setup and concurrent-run effects. The protobuf case alone showed no consistent speedup, so this does not establish an equal per-case saving or a corresponding CI wall-clock saving. Both alternating pairs show a substantial aggregate test-duration reduction.

## Cases and repeated passes

Lists extracted from `=== RUN` / `--- PASS` in Bazel test output (identical in all four batches):

Before:
- TestExplainJsonOutputIsClean
- TestExplainProtoOutputIsClean

After:
- TestExplainJsonOutputIsClean
- TestExplainProtoOutputIsClean

Each case passed 10/10 before and 10/10 after; all four remote invocations succeeded. No failures were observed; these repetitions cannot establish absence of all flakes.

Raw logs on this VM: /tmp/explain-before.log, /tmp/explain-after.log, /tmp/explain-before2.log, /tmp/explain-after2.log.

Remote invocation links:
- before: https://app.buildbuddy.io/invocation/8a693462-d5b9-40ac-8082-f1ed38ad4b04
- after: https://app.buildbuddy.io/invocation/d908915e-dced-4024-95d6-d3d5483624a2
- before2: https://app.buildbuddy.io/invocation/5437427d-846f-4e6a-8d29-e06b987280c8
- after2: https://app.buildbuddy.io/invocation/b962d028-e358-4761-95eb-7b66d0714ab2

Chose not to parallelize cases, alter shared testcli helpers, remove transitive Bazel dependencies, or change assertions. result.md is intentionally uncommitted.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3