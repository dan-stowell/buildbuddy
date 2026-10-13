# ClickHouse shared test server

Implemented an opt-in, zero-value `testclickhouse.SharedServer`, owned by TestMain via `os.Exit(server.Run(m))`. It lazily starts one native process per binary, creates a unique `buildbuddy_test_N` database for each testing.TB, and reuses that database only for repeated configuration of the same test. All eight execution-search tests now opt in. Existing Configure/GetOrStart/Start callers retain dedicated processes, the original database name, and 100ms polling. The shared server polls at 10ms with the existing 15s startup bound and one-second per-attempt bound. Process termination, bounded shutdown, reaping, and startup diagnostics remain in place.

Each fresh DB still goes through the production clickhouse.Register and schema.RunMigrations path. The measured baseline and changed runs each logged 40 real migration calls (8 databases x 5 runs). No migration cloning, seeded schema, assertions removed, cases skipped, tags changed, or parallelization.

## Re-verified premise and timings

The original unsharded target contains eight top-level tests, each calling Configure once; GetOrStart caches by testing.TB, so it starts eight processes per binary. The new TestMain-owned server starts one, saving seven starts and seven process shutdowns. A significant part of the measured saving is avoiding per-test process shutdown waits; the five-second shutdown bound itself was not shortened.

All runs used BuildBuddy Remote Bazel, with the requested public fetch URL and mirror push URL. No local fallback was needed. For the comparison, the original scoped source/BUILD files were temporarily restored, then the final sources restored. Both measurements used exactly:

```sh
bb remote test //enterprise/server/execution_search_service:execution_search_service_test \
  --runs_per_test=5 --nocache_test_results --test_output=all --test_summary=detailed
```

Bazel's test-action durations include TestMain/process shutdown and exclude compilation, analysis and runner queueing. These are the primary comparison, not sums of individual Go case times.

| Remote test-action duration | Before | After |
|---|---:|---:|
| Average of 5 runs | 45.2s | 8.4s |
| Minimum | 45.1s | 8.3s |
| Maximum | 45.2s | 8.4s |
| Standard deviation (Bazel rounded) | 0.1s | 0.0s |
| Entire Bazel invocation elapsed (includes build/analysis) | 50.889s | 26.295s |

Average saving: 36.8s per binary, approximately 81.4%, or 5.38x faster. Build/analysis costs differ, so the full invocation elapsed times are secondary context.

Baseline invocation: https://app.buildbuddy.io/invocation/05c518b4-aae2-4037-ba65-3afd92388f2a

Changed invocation: https://app.buildbuddy.io/invocation/81d690ca-2091-4aed-8c7d-55243f53081e

Raw logs: /tmp/clickhouse-before-measured.log and /tmp/clickhouse-after-measured.log.

## Before/after cases and repeated passes

The following lists were extracted from Bazel's actual Go test output (`--- PASS:`), not inferred from source. Each listed case passed 5/5 in each measured run. The sets are identical; there are no nested cases in this target.

Before:

```
TestSearchExecutions
TestSearchExecutions_SkipsEmptyInvocationUUID
TestSearchExecutions_Pagination
TestSearchExecutions_PaginationWithEmptyInvocationUUIDs
TestGetExecutionTimeline
TestGetExecutionTimeline_RequiresTarget
TestGetExecutionTimeline_RequiresAuth
TestSearchExecutions_RequiresAuth
```

After:

```
TestSearchExecutions
TestSearchExecutions_SkipsEmptyInvocationUUID
TestSearchExecutions_Pagination
TestSearchExecutions_PaginationWithEmptyInvocationUUIDs
TestGetExecutionTimeline
TestGetExecutionTimeline_RequiresTarget
TestGetExecutionTimeline_RequiresAuth
TestSearchExecutions_RequiresAuth
```

Both measured invocations report 40 passing, 0 skipped, 0 failing. An initial separate baseline and changed run also each passed all 40 case executions. The first implementation build was rejected by nogo for a backwards slice loop; it was corrected to slices.Backward before the passing runs (no test failure).

Additional final-source verification:

- Shared-harness tests: 5/5 Bazel runs, all four reported cases passing: TestSharedServerDatabaseIsolation, TestSharedServerDatabaseIsolation/first, TestSharedServerDatabaseIsolation/second, TestStartRemainsIsolated. These check same-test reuse, distinct databases on the same server, empty schema before writes, synchronous database removal, and legacy callers using a separate server and original database name.
- Unchanged invocation-search target: 5/5 Bazel runs. All reported cases passed: TestNonOLAPQuery; TestGroupIDDefaultsToAuthenticatedGroupID and its nil_query, empty_query, nonempty_query, unauthenticated subtests; TestBlendedOLAPQuery; TestUnblendedOLAPQuery. Combined harness/compatibility output: 60 passing, 0 skipped, 0 failing. https://app.buildbuddy.io/invocation/144ebe35-664e-4f12-aaf7-9281d31853a5 (/tmp/clickhouse-compat.log).
- Both changed targets also ran with `--test_arg=-test.count=5 --test_arg=-test.shuffle=on`, repeating cases within a single binary/process. Every execution-search case and every harness case passed five times; 60 Go PASS case records (including parent/subtest records), none skipped or failed. Bazel’s XML summary deduplicates these repeated names and reports 12 passing unique cases. This checks increasing database names, removal of per-test cache entries and independence from case order. https://app.buildbuddy.io/invocation/4ad31b69-1860-469d-bca5-7dbda8696f30 (/tmp/clickhouse-inprocess.log).
- gofmt and git diff --check completed successfully; Bazel builds include nogo analysis.

## Isolation and limits

Every execution-search case retains its own testenv (including transactional DB and authenticator) and now receives a separately named ClickHouse database. Existing randomized invocation/execution IDs remain intact. Reused group/user names reside in separate environments/databases, so they cannot leak across tests. Per-test shutdown hooks finish before DROP DATABASE SYNC, and database cleanup is registered before migrations run, so even a failed migration/test gets cleanup. Unique monotonic names prevent a failed cleanup from exposing another case's data. Tests share only the native server process; no test depends on another's leftovers. TestMain stops/reaps the process and removes its directory after the suite, including ordinary test failures.

The new tests intentionally inspect fixture cleanup, rather than relying on another execution-search case's leftovers. No execution-search case is a startup-specific or migration-specific scenario; all retain real migrations on an empty database. A crash of the shared server could still affect later cases, as with any shared process fixture. Five repeated remote runs plus shuffled in-process repeats passed; this is evidence against flakes, not a guarantee.

Chose not to parallelize cases because Configure sets process-global flags. Did not migrate other callers or alter their readiness interval. Did not change the 15s startup timeout or five-second shutdown bound. Did not test every other ClickHouse consumer; the unchanged invocation-search target and explicit legacy API test cover compatibility. Only scoped source/test/BUILD files were edited; this report is left git-ignored and uncommitted. No push or PR.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3