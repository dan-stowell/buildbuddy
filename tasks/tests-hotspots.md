# Task (survey, no PR): measured per-case hotspots in the slowest test targets

## Context
BuildBuddy monorepo (Bazel). The owner wants **faster tests, especially slow integration tests, while preserving coverage**. Earlier surveys estimated savings from code reading, and roughly a third of the premises were wrong once measured. This survey **measures first**.

Already done or in flight, so skip these (open PRs on github.com/dan-stowell/buildbuddy, branches t-*): raft txn split-retry top-level tests; raft metadata/client/sender/store shard counts; remote_bazel shard split + polling; CLI integration 8 shards; stream_run_logs 5 shards; ociruntime networking top-level tests; workflow polling cap; pebble janitor wait; dsingleflight barrier; server_notification readiness; cli compact parallel; raft client ErrorCodes synctest; disk_cache timestamp readiness; podman Exec timeout; executor download fixture; remote_execution sleep gates; testserver readiness 50ms; rbetest registration 10ms; hit_tracker/build_logs/workflow-service waits; bazelrbe_prober pre-extracted installs; ci_runner app hoist; clickhouse shared process for execution_search; cli explain fixture; postgres SQL reset; testenv borrowed DB for rbetest.

## How to measure: BuildBuddy Remote Bazel (don't use local bazel; it fills the disk)
One-time setup: `git remote set-url origin https://github.com/dan-stowell/buildbuddy.git && git remote set-url --push origin https://github.int.exe.xyz/dan-stowell/buildbuddy.git`. Then `bash -lc 'cd ~/repo && bb remote test <targets> --nocache_test_results ...'`. To get per-case timings, fetch the test.xml outputs (e.g. `bb remote --script='...'` to run bazel and cat the XML files from bazel-testlogs, or use `--test_output=all` together with the Go test `-test.v` output).

## What to do
1. Pick the ~25 slowest test targets in CI's normal test set (check `.github/workflows` and the `tests-ci`/`tests-remote` configs; exclude performance benchmarks and manual targets). Measure each target's wall time and per-case durations, uncached, on current master (2–3 runs).
2. For the top targets, find **why** the slow cases are slow: fixed sleeps, waiting out timeouts, real startup costs, huge fixtures, serial work that could be parallel, shard imbalance (one case dominating a shard), retries. Read the code for the top cases and confirm with timing (e.g. temporary logging inside a bb remote run).
3. Propose coverage-preserving tasks.

## Rules
- Research only: no PRs, no pushes, don't commit result.md. Restore the origin fetch URL to https://github.int.exe.xyz/dan-stowell/buildbuddy.git at the end.
- **Preserving coverage** means: don't delete, skip or weaken anything. Allowed: sharding/balancing, shared setup with per-case isolation, condition waits instead of sleeps, shorter test-only timeouts that exercise the same path, parallelizing independent cases, removing proven duplicates.

## Deliverable: result.md
1. A table of measured target wall times and their top cases.
2. Candidate tasks: `slug | what to do | files | measured slow part (s) | expected saving | coverage argument | risk | how to verify`, 10–25 rows, disjoint files (flag shared test infrastructure), ranked by measured saving.
3. Rejected candidates, and why.
