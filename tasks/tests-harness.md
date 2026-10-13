# Task (survey, no PR): integration test harness, where the setup time goes
## Your area
The shared integration test infrastructure, and the slow integration suites that use it. Start from `enterprise/server/test/integration/`, `server/testutil/` and `enterprise/server/testutil/` (e.g. testbazel, buildbuddy/buildbuddy_enterprise app runners, testexecutor, testgit, test DBs/redis/minio/clickhouse helpers), and `cli/test/integration` if it exists.
- For each harness: what does starting it cost? (Server binary launch, port waits, DB migration, bazel binary/install extraction, workspace creation, executor registration, docker pulls, etc.) Time a few representative tests locally and break the time down by phase (add temporary timing logs if needed).
- Find per-test setup that could be per-package (one server shared by all test cases instead of one per case), where isolation can be preserved via namespacing (separate instance names, separate groups/API keys).
- Find bazel-in-bazel tests: the cost of starting a nested Bazel (server startup, repository fetches, analysis), and whether nested workspaces could share an output base, disk cache, or repository cache, or use a `--batch`/startup-option trick.
- Find readiness waits implemented as fixed sleeps or slow polls in the harness.

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
