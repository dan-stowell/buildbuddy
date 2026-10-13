# bazelrbe_prober pre-extracted installs

Changed only BUILD and bazelrbe_prober_test.go in this directory. Added the existing Bazel 6.5.0 and 7.7.1 extracted-installation targets as data runfiles and injected their paths. The prober receives a quoted --install_base through its existing --bazel_startup_options flag. Following testbazel, symlinked install files are physically copied and file mtimes are set ten years into the future. Private copies are made owner-writable so Go's temporary-directory cleanup works with read-only Bazel build outputs.

Premise verified: the original tests used fresh HOME directories with no install_base override. Baseline logs contain ten “Extracting Bazel installation...” messages (two versions x five runs). Final logs contain zero. Pre-extraction moves this work to a reusable build action; it does not eliminate the initial build action or runfile preparation cost.

## Measurement

All tests ran with BuildBuddy Remote Bazel, not locally. Both measurements used the same command from a login shell:

```
bb remote test //enterprise/server/test/integration/bazelrbe_prober:bazelrbe_prober_test --runs_per_test=5 --nocache_test_results --test_output=all
```

Timing method: Go's reported case duration from the Bazel test output, including fixture startup, install preparation, prober execution and cleanup. Five repetitions were scheduled by Bazel in each invocation. This is a small before/after sample on remote runners, with concurrent repetitions and variable load; it is not a controlled extraction-only benchmark.

| Case | Before samples (s) | After samples (s) | Mean before | Mean after | Mean saving |
| --- | --- | --- | --- | --- | --- |
| Bazel6 | 11.76, 12.18, 12.40, 12.42, 12.34 | 9.44, 9.42, 9.12, 9.70, 7.96 | 12.220s | 9.128s | 3.092s (25.3%) |
| Bazel7 | 13.69, 13.31, 13.32, 13.54, 13.65 | 10.63, 11.52, 12.08, 11.48, 13.71 | 13.502s | 11.884s | 1.618s (12.0%) |

Sum of case means: 25.722s -> 21.012s, saving 4.710s (18.3%). One Bazel7 sample was slower than the baseline range, so do not interpret this as a guarantee for each invocation. Outer Bazel elapsed time was 128.645s before and 101.827s after; build/cache differences confound that figure, so it is not the claimed test saving.

Baseline: https://app.buildbuddy.io/invocation/5031a590-08f1-4c5f-bb44-be9285667d53

Final: https://app.buildbuddy.io/invocation/1b2290b9-2541-490c-82a8-44a155d3c141

Local captured logs: /tmp/prober-before.log and /tmp/prober-after-fixed.log.

## Coverage and repetitions

Exact case lists extracted from `=== RUN` lines in the before and after Bazel test output:

| Before | After | Before outcome | Final outcome |
| --- | --- | --- | --- |
| TestBazelRBEProber_Bazel6 | TestBazelRBEProber_Bazel6 | 5/5 pass | 5/5 pass |
| TestBazelRBEProber_Bazel7 | TestBazelRBEProber_Bazel7 | 5/5 pass | 5/5 pass |
| TestBazelRBEProber_Bazel8 | TestBazelRBEProber_Bazel8 | 5/5 existing skip | 5/5 existing skip |

The case sets are identical. Every active case passed each of the five final repetitions; Bazel reported the target passing. Bazel8 was already skipped for implicit network dependencies; it was not enabled, deleted, newly skipped, or weakened by this change. No assertion, scenario, target/input count, timeout, or build argument was removed. The quoted-value/whitespace parsing scenario remains intact.

An initial changed run failed 5/5 repetitions solely because t.TempDir cleanup could not remove read-only directories in the copied installs; the prober builds themselves succeeded. This was fixed by adding chmod on the private copies, and the entire five-run command was rerun successfully. Initial failed invocation: https://app.buildbuddy.io/invocation/a8461cab-526d-41ad-bea8-4d9598fbdce4 (log /tmp/prober-after.log). Final results above include that fix.

Isolation: no server/executor fixture sharing was introduced. Each case still creates its own RBE environment, API key, HOME and temporary prober workspace/output base. Copied installs live in a case-specific temporary directory; direct runfile installs are version-specific immutable content apart from pristine mtimes, following testbazel. Cases do not rely on other cases' leftovers.

No production prober changes, Bazel8 network/coverage changes, fixture sharing, timeout changes, or unrelated optimizations were attempted. No local fallback was necessary. This report is git-ignored and is not committed.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3