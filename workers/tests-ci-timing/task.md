# Task (survey, no PR): which tests are slow, measured from CI
## Your area
Get real per-target test durations from upstream CI, then rank them.
- **Data sources:** public GitHub Actions runs of `buildbuddy-io/buildbuddy` (`gh run list --repo buildbuddy-io/buildbuddy --workflow ...`, `gh run view --log`; Bazel's test summary prints `//target  PASSED in 123.4s`, along with shard and attempt info), plus the CI workflow and bazelrc configs (`.github/workflows/`, `.bazelrc`, `shared.bazelrc`; the `tests-ci`/`tests-remote` configs, test tags, `--test_tag_filters`). Also look at whether CI streams to a public BuildBuddy invocation page with a timing tab you can read without auth. Don't use any API key.
- Collect at least 5 recent successful master runs. Per test target, report the median duration, shard count, size/timeout, tags (e.g. `docker`, `bare`, `performance`, `manual`), flaky retries, and which CI job runs it.
- Rank the top 40 by (a) wall time (the critical-path tests that set how long CI takes) and (b) total test time. Mark which ones are integration tests.
- Find structural issues: timeouts far above actual runtime; tests marked `size=enormous`/`large` that run in 10s; big tests that aren't sharded; tests that are flaky and retried (retries cost time); serial jobs that could run in parallel.

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
