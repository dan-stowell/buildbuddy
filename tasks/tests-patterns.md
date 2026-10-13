# Task (survey, no PR): time-wasting patterns inside test code
## Your area
Test bodies across the repo (Go `*_test.go`, plus TS tests).
- **Fixed sleeps:** `time.Sleep` in tests and in test helpers, and production code whose timing knobs (poll intervals, retry backoff, TTLs, flush periods, heartbeat intervals, lease durations) tests run with at production defaults. List each one with file:line, the duration, and how often it runs, and propose a condition wait, fake clock (the repo may already use a clockwork-style fake clock: check), or a test-only flag override.
- **Slow polling:** `Eventually`-style loops with large intervals, and retry loops with long backoff.
- **Timeouts that are hit on purpose:** tests that wait for a timeout to fire (e.g. a 5s or 30s context deadline) where a shorter test-configured timeout would exercise the same path.
- **Serial cases that could run in parallel:** suites without `t.Parallel()` where cases are independent; table tests with heavy per-case setup.
- **Big fixtures:** generating or uploading large random data where smaller data covers the same logic (be careful: some tests deliberately cross size thresholds, such as chunking or compression cutoffs, so keep those).
- Rank by estimated saved seconds (duration × occurrences), and spot-check the top ones by timing them locally.

## Context
BuildBuddy monorepo (Bazel; Go server and executor, TS app, `enterprise/`). The owner wants **faster tests, especially the slow integration tests, while preserving coverage**. Your survey feeds a backlog that ~20 parallel engineers will each turn into one small PR.

**Preserving coverage means:** a speedup must not drop or weaken assertions, scenarios, or code paths exercised. Acceptable: sharing expensive setup across cases, replacing fixed sleeps with readiness or condition waits, shorter poll intervals or timeouts *in tests*, parallelizing independent cases, sharding, faster fakes where the real dependency isn't what's under test (only if another test still covers the real one), removing **proven** duplicate tests (the same code path and assertions, shown concretely). Not acceptable: deleting slow tests, marking them manual or skipped, or loosening assertions.

## Rules
- **Research only.** No PRs, no pushes. Small local experiments are encouraged (e.g. change a sleep and time the test), but don't commit them.
- **Use numbers.** Every claim needs the command and its output. This VM has only 2 CPUs and no remote execution, so local timings are slow and noisy: compare before and after on the same VM, and say which numbers are local and which come from CI.
- Run long commands in the background (`nohup ... &`) and poll; don't abandon a slow build. If something truly can't run here (needs Docker/Firecracker/KVM/cloud creds), say so and reason from the code and CI data instead.

## Deliverable: `result.md`
1. **Summary:** the biggest test-time wins, with estimated savings (wall time and total test time).
2. **Measurements:** raw data and the commands used.
3. **Candidate tasks:** a table: `slug | what to do | files/dirs | expected saving (s, which tests) | coverage argument (why nothing is lost) | risk (flakiness!) | how to verify`. 10–30 rows, ranked by saving per effort. Rows must touch disjoint files. Flag rows that touch shared test infrastructure (testutil, harness packages, .bazelrc, CI config), because those must be serialized.
4. **Rejected:** what you looked at and why it isn't worth doing, or would lose coverage.
