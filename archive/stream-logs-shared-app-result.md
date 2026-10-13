# stream_run_logs fixture investigation

Stopped after verifying the premise: sharing cannot remove any app startups in the requested five-shard configuration. No source files, cases, assertions, timeouts, or scenarios were changed. An empty commit records the investigation; this report is not committed.

## Configuration and method

This checkout (770b88bc89c0b20059d42a237e8c7923ff5b7215) does **not** yet have `shard_count = 5` in the target BUILD file. To measure the configuration described in the task without editing the out-of-scope BUILD file, I forced five shards on the command line:

```sh
bb remote test //enterprise/server/test/integration/cli/stream_run_logs:stream_run_logs_test --test_sharding_strategy=forced=5 --runs_per_test=5 --nocache_test_results --test_output=all
```

Used BuildBuddy Remote Bazel, not local Bazel. Remote runner started within two minutes. Runner: https://app.buildbuddy.io/invocation/fe844533-4dc3-4073-90c6-6045c982fe96 ; Bazel results: https://app.buildbuddy.io/invocation/7223c8db-fd43-47ef-8234-1a85a2ed4233 . Full output is saved locally at `/tmp/stream-sharded-baseline.log` (ANSI-clean copy at `/tmp/stream-sharded-baseline.clean.log`). An initial unsharded remote request was canceled while waiting for a runner; it supplies no measurement.

Bazel output confirms exactly one top-level case per shard: shard 1 WithBuild, 2 FailingCommand, 3 FlushingMultipleChunks, 4 UploadFailure (one nested warn case), 5 SignalForwarding. Each case calls setup once, which calls buildbuddy_enterprise.Run once; that helper starts Redis and the enterprise app. Thus there is already exactly one app/Redis startup per test process, including the process containing the nested upload-failure case. Repetitions are separate processes and cannot share this fixture.

## Timings and repeat passes

Before measurements below are Go test PASS durations from the remote Bazel output, including each case's setup and cleanup. Values are seconds. All five repetitions passed for every case; all 25 shard executions passed, with no failures reported.

| Case | Five before durations (s) | Median (s) | Pass count |
| --- | --- | --- | --- |
| TestWithBuild | 29.97, 30.69, 31.61, 30.92, 31.13 | 30.92 | 5/5 |
| TestFailingCommand | 32.09, 19.54, 19.45, 20.31, 22.26 | 20.31 | 5/5 |
| TestFlushingMultipleChunks | 0.64, 0.65, 1.20, 0.72, 2.95 | 0.72 | 5/5 |
| TestUploadFailure | 2.95, 0.79, 0.77, 1.19, 1.37 | 1.19 | 5/5 |
| TestUploadFailure/failure_mode=warn | 2.95, 0.79, 0.77, 1.18, 1.37 | 1.18 | 5/5 |
| TestSignalForwarding | 23.98, 25.41, 24.28, 24.59, 25.07 | 24.59 | 5/5 |

Overall remote Bazel elapsed time: 174.267s; critical path: 51.91s (includes build and all repetitions). The nested row is included in its parent duration, not additional work.

After timings: **not measured**, because the task explicitly asks to stop when sharing does not help per shard. There is no changed implementation to benchmark. Measured case allocation establishes zero redundant fixture startups per shard; zero removable startups is a structural conclusion, not a claimed before/after speedup. No claim is made about the actual performance of a hypothetical shared implementation or about unsharded savings.

## Before and after case lists

Before list, extracted from `=== RUN` and `--- PASS` in the remote Bazel output:

```text
TestWithBuild
TestFailingCommand
TestFlushingMultipleChunks
TestUploadFailure
TestUploadFailure/failure_mode=warn
TestSignalForwarding
```

After list (identical source and tested revision; no separate after invocation):

```text
TestWithBuild
TestFailingCommand
TestFlushingMultipleChunks
TestUploadFailure
TestUploadFailure/failure_mode=warn
TestSignalForwarding
```

Same set; every case passed five times. The existing TODO for FailureModeFail was left unchanged; it is not an existing executed scenario.

## Isolation and decisions

No fixture sharing was introduced. Existing isolation is preserved: every case retains its fresh enterprise app, Redis instance and database, its own web login client/cookie jar, API key, UUID invocation ID, workspace and connections. Connections retain their existing case cleanup. No case acquires a dependency on another case's leftovers or failure.

Did not consolidate cases under a parent test, reduce shards, or edit BUILD: doing so would alter the five-shard configuration being evaluated and could sacrifice its parallelism. Did not implement a shared fixture for an unsharded configuration because the requested per-shard saving is absent. No flaky optimization was introduced. Restored origin fetch URL to the internal mirror; no push or PR.

