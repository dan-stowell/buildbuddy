# CI test duration survey

Research date: 2026-10-13 UTC. Checkout: `770b88bc89c0b20059d42a237e8c7923ff5b7215` (2026-10-09). No commits, PRs, pushes, API keys, or production changes.

## 1. Summary

**The first backlog targets should be the always-running CLI / hosted-Bazel tests, and the 60-second action-merging command.** Shard independent cases instead of weakening them. Promote large nested matrices to independent top-level tests so existing Go test sharding can distribute them.

Important source limitation: GitHub Actions exposes run/job/step timings anonymously, but denies log downloads. Consequently, **the per-target numbers below come from public BuildBuddy workflow invocations on the same five master commits, not GitHub's `CI / build` invocations**. Both are upstream CI; their flags differ. GitHub `CI / build` has median **225 s job time / 207 s Test-step time**. BuildBuddy's race-enabled `Test` has median **54.715 s Bazel invocation time**. Do not use one as an estimate of the other. The requested GitHub-only per-target ranking remains unavailable without authorized GitHub log access; this report supplies the anonymous upstream-CI alternative and preserves that distinction throughout.

Largest concrete opportunities, with conditional savings rather than unvalidated promises:

* **Current warm-run critical path:** timing-tab profiles put `remote_bazel_test` shard 4 on the full Bazel critical path in **5/5 runs**. It finishes last among freshly executed tests in 4/5 main runs; `cli_test` finishes last in the other. Their median longest-shard durations are **42.163 s** and **38.637 s**. Splitting the hosted-Bazel critical shard's two independent tests could save **0–13.04 s** of target wall time, using the latest run's XML case timings. CLI sharding 4→8 has an idealized **0–19.32 s** target-wall saving. These savings are not additive: after hosted-Bazel improves, CLI can become the bottleneck. Sharding preserves every case and usually adds setup / total time.
* **Cold integration runs:** `TestActionMerging_LongTask` has a **72.61 s** recorded XML case, starts `sleep 60`, and performs its merging assertion after **350 ms**, against a **250 ms** lease TTL. Keep the beyond-TTL / lease-renewal scenario, hold the task with a controlled command, then explicitly release and await it. The removable command wait is approximately **59.65 s total test time**. On the latest retained shard durations, changing this case alone lowers target maximum **117.466→97.770 s**, an upper estimate of **19.696 s wall**; the custom-resources matrix is the next bottleneck. Both changes belong to one engineer because they touch the same file. Savings are **0 s in the sampled warm runs**, where the target is cached.
* **Cold unsharded tests:** raft metadata **136.42 s**, codesearch index **120.43 s**, pebble cache **78.90 s**, raft client **55.66 s**, and raft sender **44.65 s** each have one effective shard. Metadata/index/client/sender have **20 / 30 / 4 / 7 top-level tests**, respectively. Metadata/index sharding 1→4 has idealized upper bounds of **102.31 / 90.32 s wall saved**, with **no total-time reduction promised**. Actual savings depend on the longest case and resource capacity.
* **Cold imbalance:** txn has **8 shards / 487.13 s total / 200.65 s maximum**; store has **8 / 868.08 / 160.07**. The latest txn shard durations are **200.651, 71.295, 46.452, 38.393, 37.786, 37.373, 28.699, 26.483 s**. Its 12-case split retry matrix creates fresh three-store clusters per nested case. Exposing those cases to top-level sharding is a stronger lead than simply increasing shard count.
* **Separate performance suite:** `Benchmark` really executes on all five commits, median invocation **598.607 s**; `cache_test` alone has median **580.746 s**. Do not shorten benchmark sampling or drop workloads to claim a coverage-preserving speedup. This is the largest freshly executed test, but needs benchmark-specific profiling before a small safe task can be assigned.

Evidence: [raw observations](survey-data/observations.csv), [all target/job medians](survey-data/target-medians.csv), [fresh completion order](survey-data/fresh-critical-path.json), [case XML index](survey-data/cases.json), and source evidence E02–E10. Forecasts in this document are arithmetic models and code-based hypotheses, **not measured before/after speedups**. No local test timing is substituted for CI timing.

## 2. Measurements and commands

### Run selection and access

Selected the five newest successful master runs of `.github/workflows/main.yaml` returned by the public API on 2026-10-12. For each commit, followed successful public commit statuses named `Test`, `Baremetal tests`, `Test (linux_arm64)`, and `Benchmark`: **20 successful master BuildBuddy invocations**. Each status points to a workflow parent whose log contains the actual child `bazel test` invocation URL. Parent workflow setup time is excluded from the Bazel durations.

```bash
gh run list --repo buildbuddy-io/buildbuddy --workflow main.yaml \
  --branch master --status success --limit 5
gh run view 38008035373 --repo buildbuddy-io/buildbuddy --log
# Both: "To get started with GitHub CLI, please run: gh auth login"

curl -s 'https://api.github.com/repos/buildbuddy-io/buildbuddy/actions/workflows/main.yaml/runs?branch=master&status=success&per_page=10'
# HTTP 200; first five run IDs in the following table.
curl -s 'https://api.github.com/repos/buildbuddy-io/buildbuddy/actions/runs/38008035373/jobs'
# HTTP 200; build=success, timestamps and Test step duration below.
curl -s 'https://api.github.com/repos/buildbuddy-io/buildbuddy/actions/runs/38008035373/logs'
# HTTP 403: {"message":"Must have admin rights to Repository.","status":"403",...}
curl -Ls 'https://github.com/buildbuddy-io/buildbuddy/actions/runs/38008035373/job/114081282031'
# HTML contains "Sign in to view logs".
curl -s 'https://api.github.com/repos/buildbuddy-io/buildbuddy/commits/5608c55aed2ac3d3df3df567299f2366964bfe2d/status'
# HTTP 200; Test target_url ends in e2f7a3f4-4bb2-4e5b-8b78-e96621befd71.
```

`gh` output: [gh-access.out](survey-data/gh-access.out). JSON outputs: [runs](survey-data/github-runs.json), [GitHub job durations](survey-data/github-job-durations.json), `survey-data/bb-*-jobs`, and `survey-data/<sha>.statuses.json`. The initial GitHub HTTP 403 and HTML message above were copied from the observed tool output. Repeating GitHub queries later hit the anonymous API rate limit; collection resumed from saved status JSON.


| GitHub CI run | Created UTC | SHA | Job s | Test step s |
|---|---|---|---:|---:|
| [38008035373](https://github.com/buildbuddy-io/buildbuddy/actions/runs/38008035373) | 2026-10-10T00:12:28Z | `5608c55aed2ac3d3df3df567299f2366964bfe2d` | 272 | 253 |
| [38007700180](https://github.com/buildbuddy-io/buildbuddy/actions/runs/38007700180) | 2026-10-10T00:07:59Z | `4335f194ef1291a3d86dfdaab2fc763fdcd8c202` | 215 | 182 |
| [38001221843](https://github.com/buildbuddy-io/buildbuddy/actions/runs/38001221843) | 2026-10-09T22:48:55Z | `56e7c943ba7a87a7a3f44f7861ea50cdf5816a92` | 225 | 207 |
| [37999060406](https://github.com/buildbuddy-io/buildbuddy/actions/runs/37999060406) | 2026-10-09T22:24:41Z | `9623431ff6bc71d74863144aeac5fcb85ef9f46c` | 143 | 115 |
| [37998947307](https://github.com/buildbuddy-io/buildbuddy/actions/runs/37998947307) | 2026-10-09T22:23:27Z | `24ecde12aababfcbc486298d5179ea273abc2ee5` | 249 | 217 |

### Public BuildBuddy data and reproducibility

The anonymous invocation page exposes the timing / targets views (the Bazel profile used by the timing tab was downloaded anonymously with HTTP 200 on all five main runs), and the same frontend read RPCs expose the structured Bazel test summaries and attempt records. No API key, cookie, login, or remote execution was used. RPC request/response encoding is taken from `app/service/rpc_service.ts`, `proto/invocation.proto`, `proto/target.proto`, `proto/eventlog.proto`, and `proto/build_event_stream.proto`. Requests use `Content-Type: application/proto` at `https://buildbuddy.buildbuddy.io/rpc/BuildBuddyService/{GetInvocation,GetEventLogChunk,GetTarget}`. This is the public application's frontend interface, not an exe.dev infrastructure endpoint. Anonymous `SearchInvocation` returns HTTP 403, so no invocation search bypass was attempted.

```bash
# Initial selection JSON above is saved as survey-data/github-runs.json.
python3 survey-data/batch.py      # five main Test invocations; logs + paginated PASSED summaries
python3 survey-data/extra.py      # five each baremetal, arm64, Benchmark; uses saved commit statuses
python3 survey-data/details.py   # detailed per-shard, per-attempt events for ranking candidates
python3 survey-data/xmls.py      # public test.xml artifacts, where the attempt exposed them
python3 survey-data/profiles.py  # public timing-tab profiles and exact critical-path components
python3 survey-data/analyze.py   # observations, medians, rankings
python3 survey-data/evidence.py  # source commands and their output
python3 survey-data/write_report.py
# Long collectors were run as:
nohup python3 survey-data/batch.py > survey-data/collection.out 2>&1 < /dev/null & wait
# Shell wait keeps the background child alive; tools yield a session ID, then it is polled.
```

All protobuf responses are saved as `survey-data/<invocation>.*.pb`. `collect.py` contains the small schema-specific protobuf wire encoder/decoder; `batch.py`/`extra.py` collect paginated `GetTarget(status=PASSED)` results; `details.py` saves exact test result IDs (run/shard/attempt), cache flags, durations and starts. All 20 invocation metadata responses were checked: **no FAILED, TIMED_OUT, or FLAKY groups**. Successful targets' highest attempt is 1, and observed retry actions are **0**. The detailed dataset covers **69 distinct labels / 355 invocation-label records**; all target summaries cover **3,820 observations / 767 job-target pairs**. Artifact downloads use the frontend `/file/download?bytestream_url=...&invocation_id=...`; **60 XML artifacts downloaded, 0 failures**, indexed in `cases.json`. XML case times can also be inherited from cached executions; they are not five fresh repetitions of every case.

Initial collection issues are preserved: requesting child inlining caused HTTP 500; child IDs were instead obtained from parent logs. Target pagination without a status filter also caused HTTP 500; filtering PASSED before pagination fixed it. `extra.py` resumed from saved JSON after GitHub rate limiting. These are collection issues, not test flakes.


| Commit / GitHub run | Job | Public Bazel invocation | Bazel s | Passed summaries |
|---|---|---|---:|---:|
| 37998947307 / `24ecde12aaba` | Baremetal tests | [a70ea422-e0db-4166-b4e7-7441976db8cd](https://buildbuddy.buildbuddy.io/invocation/a70ea422-e0db-4166-b4e7-7441976db8cd) | 2.124 | 1 |
| 37998947307 / `24ecde12aaba` | Benchmark | [b58e05b5-d655-4532-897d-ecedbf4a9e4e](https://buildbuddy.buildbuddy.io/invocation/b58e05b5-d655-4532-897d-ecedbf4a9e4e) | 598.607 | 3 |
| 37998947307 / `24ecde12aaba` | Test (linux_arm64) | [417d69f1-e35f-44c8-929e-df2c1a2d3a3d](https://buildbuddy.buildbuddy.io/invocation/417d69f1-e35f-44c8-929e-df2c1a2d3a3d) | 3.402 | 26 |
| 37998947307 / `24ecde12aaba` | Test | [1ad5c1b1-4534-4287-9bed-781ebfa4f1a1](https://buildbuddy.buildbuddy.io/invocation/1ad5c1b1-4534-4287-9bed-781ebfa4f1a1) | 54.495 | 732 |
| 37999060406 / `9623431ff6bc` | Baremetal tests | [d67a8856-2b0d-422d-a9b5-b23119479061](https://buildbuddy.buildbuddy.io/invocation/d67a8856-2b0d-422d-a9b5-b23119479061) | 3.092 | 1 |
| 37999060406 / `9623431ff6bc` | Benchmark | [a3e2086d-1bab-45c1-bb8a-6c45da580aa3](https://buildbuddy.buildbuddy.io/invocation/a3e2086d-1bab-45c1-bb8a-6c45da580aa3) | 583.911 | 3 |
| 37999060406 / `9623431ff6bc` | Test (linux_arm64) | [a2878d7f-ae45-436e-a7db-591de0effc54](https://buildbuddy.buildbuddy.io/invocation/a2878d7f-ae45-436e-a7db-591de0effc54) | 3.431 | 26 |
| 37999060406 / `9623431ff6bc` | Test | [a2f258c6-53cb-4080-b334-9bcdf3012cf5](https://buildbuddy.buildbuddy.io/invocation/a2f258c6-53cb-4080-b334-9bcdf3012cf5) | 54.715 | 732 |
| 38001221843 / `56e7c943ba7a` | Baremetal tests | [e1b79aac-b2d2-4fd7-992a-6a7ad966d7ee](https://buildbuddy.buildbuddy.io/invocation/e1b79aac-b2d2-4fd7-992a-6a7ad966d7ee) | 4.158 | 1 |
| 38001221843 / `56e7c943ba7a` | Benchmark | [3230ca11-6ca5-4b93-b76d-926216b8ec8d](https://buildbuddy.buildbuddy.io/invocation/3230ca11-6ca5-4b93-b76d-926216b8ec8d) | 571.440 | 3 |
| 38001221843 / `56e7c943ba7a` | Test (linux_arm64) | [321fd961-065e-4f96-af06-5296e13554e2](https://buildbuddy.buildbuddy.io/invocation/321fd961-065e-4f96-af06-5296e13554e2) | 6.426 | 26 |
| 38001221843 / `56e7c943ba7a` | Test | [135ff94f-2e6e-4007-8418-e940498aa09b](https://buildbuddy.buildbuddy.io/invocation/135ff94f-2e6e-4007-8418-e940498aa09b) | 55.813 | 732 |
| 38007700180 / `4335f194ef12` | Baremetal tests | [ca65ca26-a350-41f2-a535-a9511f00c3c7](https://buildbuddy.buildbuddy.io/invocation/ca65ca26-a350-41f2-a535-a9511f00c3c7) | 2.750 | 1 |
| 38007700180 / `4335f194ef12` | Benchmark | [e16aad81-1857-4584-bc6d-4ce86f67a869](https://buildbuddy.buildbuddy.io/invocation/e16aad81-1857-4584-bc6d-4ce86f67a869) | 616.662 | 3 |
| 38007700180 / `4335f194ef12` | Test (linux_arm64) | [9692fece-b187-4166-9d9e-038e08078016](https://buildbuddy.buildbuddy.io/invocation/9692fece-b187-4166-9d9e-038e08078016) | 3.932 | 26 |
| 38007700180 / `4335f194ef12` | Test | [5c515b99-9a6d-46e1-9af2-86a6ec648fd1](https://buildbuddy.buildbuddy.io/invocation/5c515b99-9a6d-46e1-9af2-86a6ec648fd1) | 52.398 | 737 |
| 38008035373 / `5608c55aed2a` | Baremetal tests | [a1c8ecf2-ce05-4af9-a101-13dbd6d7e8d2](https://buildbuddy.buildbuddy.io/invocation/a1c8ecf2-ce05-4af9-a101-13dbd6d7e8d2) | 3.713 | 1 |
| 38008035373 / `5608c55aed2a` | Benchmark | [25d347b5-ca6e-4b64-b41d-269863c7bf8a](https://buildbuddy.buildbuddy.io/invocation/25d347b5-ca6e-4b64-b41d-269863c7bf8a) | 643.677 | 3 |
| 38008035373 / `5608c55aed2a` | Test (linux_arm64) | [5d28fd0d-5cbf-469e-b3c3-f5058e44affc](https://buildbuddy.buildbuddy.io/invocation/5d28fd0d-5cbf-469e-b3c3-f5058e44affc) | 4.988 | 26 |
| 38008035373 / `5608c55aed2a` | Test | [79286043-87d4-4a10-b2a0-941260c61641](https://buildbuddy.buildbuddy.io/invocation/79286043-87d4-4a10-b2a0-941260c61641) | 70.394 | 737 |

### Actual critical paths from timing-tab profiles

`python3 survey-data/profiles.py` downloaded each main invocation's `command.profile.gz` using the same public `/file/download` endpoint as `app/invocation/invocation_timing_card.tsx`; HTTP 200 for all five, no authentication. Exact components are in [profiles.json](survey-data/profiles.json), with commands/output in [profiles.out](survey-data/profiles.out), and compressed raw profiles in `survey-data/<invocation>.profile.gz`.

All five profile outputs (action time includes action/scheduling overhead):

| GitHub commit run | HTTP | Profile critical path s | Hosted-Bazel shard 4 action s |
|---|---:|---:|---:|
| 37998947307 | 200 | 50.866798 | 43.843430 |
| 37999060406 | 200 | 49.944557 | 42.836362 |
| 38001221843 | 200 | 51.073278 | 41.680838 |
| 38007700180 | 200 | 48.637230 | 43.393883 |
| 38008035373 | 200 | 66.409935 | 60.853720 |

**Hosted-Bazel shard 4 is on all five full critical paths.** The latest-finishing fresh test is CLI in one run because start times/build prerequisites differ; that alone does not define the longest dependency path.

Latest run output:

```text
Anonymous timing-tab profile download HTTP 200 bytes 428694 trace events 43383
BazelWorkspaceStatusAction: 0.038131s
populate_commit_impl: 0.453679s
GoCompilePkg server/version: 0.683425s
GoCompilePkg server/util/statusz: 0.000450s
GoLink cli/cmd/bb: 4.380313s
runfiles remote_bazel_test: 0.000217s
Testing remote_bazel_test (shard 4 of 7): 60.853720s
# Sum: 66.409935s, matching console Critical Path: 66.41s.
```

The critical test action includes scheduling/action overhead, so **60.853720s profile action time** differs from **59.566s BEP test-attempt runtime**. This confirms the test is on the latest full build critical path; the top-40 wall table still consistently uses test-attempt durations.

### Definitions, cache treatment, and scope

* **Wall** in the rankings = median of the longest **test attempt** per invocation. This identifies potential execution bottlenecks when a target actually runs. It excludes compilation, queueing, provisioning, and time between attempts; it is not a full Bazel critical-path trace. The CSV also reports the target envelope `last_stop − first_start`, which includes scheduling gaps.
* **Total** = median `TestSummary.total_run_duration` per invocation, the sum over shards/runs/attempts. It is test duration, not CPU seconds and not job wall time. `Target.Timing.Duration` also represents total duration; the collector deliberately renames it `target_timing_s` to avoid treating a sharded sum as wall time.
* `fresh_runs/n` distinguishes newly executed test actions from cache hits. Every ranked target has **n=5**. Cached durations describe the executions stored in the cache. Their repeated values are not independent measurements and **do not represent time consumed in the current run**. `unique_result_sets` and per-attempt cache flags are retained in the data. No fresh median exists for targets showing `0/5`.
* Missing BEP size enums are resolved from the BUILD declaration; undeclared native `go_test` size defaults to medium. Webdriver-generated targets default to large, and add `webdriver,native`; their enclosing suite / wrapped Go test can separately be `manual`. Those `manual` wrapper tags do not imply that the measured browser target was skipped.
* Effective timeout comes from the rule's timeout category (or size default), then the job's `--test_timeout`. Race workflows use **short=120 / moderate=600 / long=900 / eternal=3600 s**; Benchmark uses **60 / 300 / 900 / 3600 s**. These are configuration-derived limits, not observed failing runtimes. Static tags / declarations refer to the checkout; the four main configuration files were fetched at the newest sampled SHA and **all four match the checkout byte-for-byte**. All sampled commits are within the preceding two hours of master changes; test source attribution to older cached executions is therefore a hypothesis, not proof of the exact original revision.
* Integration marking includes explicit `/test/integration/`, webdriver end-to-end targets, real raft clusters, container runtimes and Docker-dependent DB tests. Other tests may also exercise multiple components; the flag is a practical classification, not an official Bazel attribute.
* Top 40 ranks combine the disjoint **Linux amd64 `Test` (L), `Baremetal tests` (B), and `Benchmark` (P)** suites. The separate arm64 job (A) has all its per-target medians in the CSV; it is excluded from these rankings to avoid mixing two platform timings or double-counting labels. macOS, Windows, and GitHub arm64 job per-target timings were not collected. Job membership in GitHub `CI / build` is inferred from `//... --config=ci` and filters; that job's per-target timings remain unknown.

Collection output: **3,820 observations; 767 job-target pairs; 5 runs per each of 4 jobs; retry actions 0** ([analysis.out](survey-data/analysis.out)). BuildBuddy `Test` logs report **Executed 7 out of 738 tests** on the latest two commits (earlier three: 733 selected). These seven targets represent **18 fresh shard attempts per run**. Median actual freshly executed main-suite test sum is **374.672 s**, versus **7,307.995 s** median of all main summaries including historical cached duration. Baremetal has **30/30 cached shards in every run**; arm64 has **0 newly executed attempts**. Do not call those historical totals current CI cost.

Example raw latest main log:

```text
Elapsed time: 70.394s, Critical Path: 66.41s
Executed 7 out of 738 tests: 737 tests pass and 1 was skipped.
```

The checkout's test summary mode does not print every passing target's `PASSED in ...` line in these logs; the structured public BEP provides those timings. Skipped or target-summary-less records are omitted from timing rankings; absent duration is never treated as zero. Selection counts are read from logs, and passed-summary counts from BEP, so the two counts intentionally differ.

### Tests that actually execute in the sampled amd64 runs

The following table includes three performance tests and seven main-suite tests. Everything else in the primary rankings was cached. `remote_bazel_test` shard 4 is last to finish in 4/5 main runs; CLI shard 2 is last in the remaining run. Build dependencies and RBE queueing can still set the full Bazel critical path.


| Rank | Target | Job | Wall s | Total s | Shards | Size / timeout s | Tags | Fresh runs / n | Retries | Integration |
|---:|---|---|---:|---:|---:|---|---|---|---:|---|
| 1 | `//enterprise/server/test/performance/cache:cache_test` | P | 580.75 | 580.75 | 1 | medium / 900 | performance | 5/5 | 0 | no |
| 2 | `//enterprise/server/test/integration/remote_bazel:remote_bazel_test` | L | 42.16 | 141.30 | 7 | medium / 600 | block-network | 5/5 | 0 | yes |
| 3 | `//cli/test/integration/cli:cli_test` | L | 38.64 | 120.16 | 4 | medium / 600 | — | 5/5 | 0 | yes |
| 4 | `//enterprise/server/test/performance/bytestream:bytestream_test` | P | 29.37 | 29.37 | 1 | medium / 900 | performance | 5/5 | 0 | no |
| 5 | `//enterprise/server/test/integration/cli/stream_run_logs:stream_run_logs_test` | L | 27.35 | 27.35 | 1 | medium / 600 | — | 5/5 | 0 | yes |
| 6 | `//cli/test/integration/compact:compact_test` | L | 18.32 | 18.32 | 1 | medium / 600 | — | 5/5 | 0 | yes |
| 7 | `//enterprise/server/test/webdriver/cli_login:cli_login_test_chromium-local` | L | 15.90 | 47.02 | 3 | large / 900 | webdriver,native | 5/5 | 0 | yes |
| 8 | `//cli/test/integration/fix:fix_test` | L | 14.87 | 14.87 | 1 | medium / 600 | — | 5/5 | 0 | yes |
| 9 | `//enterprise/server/remote_execution/copy_on_write:copy_on_write_benchmark_test` | P | 13.80 | 13.80 | 1 | medium / 300 | performance | 5/5 | 0 | no |
| 10 | `//cli/test/integration/explain:explain_test` | L | 5.48 | 5.48 | 1 | medium / 600 | — | 5/5 | 0 | yes |


### Top 40 by longest-attempt wall time

| Rank | Target | Job | Wall s | Total s | Shards | Size / timeout s | Tags | Fresh runs / n | Retries | Integration |
|---:|---|---|---:|---:|---:|---|---|---|---:|---|
| 1 | `//enterprise/server/test/performance/cache:cache_test` | P | 580.75 | 580.75 | 1 | medium / 900 | performance | 5/5 | 0 | no |
| 2 | `//enterprise/server/raft/txn:txn_test` | L | 200.65 | 487.13 | 8 | medium / 600 | block-network | 0/5 | 0 | yes |
| 3 | `//enterprise/server/remote_execution/containers/firecracker:firecracker_test` | B | 195.53 | 1404.91 | 30 | medium / 900 | bare | 0/5 | 0 | yes |
| 4 | `//enterprise/server/raft/store:store_test` | L | 160.07 | 868.08 | 8 | medium / 600 | block-network | 0/5 | 0 | yes |
| 5 | `//enterprise/server/raft/metadata:metadata_test` | L | 136.42 | 136.42 | 1 | medium / 600 | — | 0/5 | 0 | yes |
| 6 | `//codesearch/index:index_test` | L | 120.43 | 120.43 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 7 | `//enterprise/server/remote_execution/containers/ociruntime:ociruntime_test` | L | 106.06 | 213.44 | 4 | medium / 600 | docker | 0/5 | 0 | yes |
| 8 | `//enterprise/server/test/integration/remote_execution:remote_execution_test` | L | 84.98 | 625.72 | 11 | enormous / 900 | cpu:8 | 0/5 | 0 | yes |
| 9 | `//enterprise/server/test/integration/podman:podman_test` | L | 81.51 | 81.51 | 1 | large / 600 | block-network,docker | 0/5 | 0 | yes |
| 10 | `//enterprise/server/backends/pebble_cache:pebble_cache_test` | L | 78.90 | 78.90 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 11 | `//enterprise/server/test/webdriver/executions_clickhouse:executions_clickhouse_test_chromium-local` | L | 78.05 | 153.44 | 2 | large / 900 | webdriver,native | 0/5 | 0 | yes |
| 12 | `//enterprise/server/backends/userdb:userdb_test_mysql` | L | 74.86 | 278.22 | 5 | medium / 600 | docker | 0/5 | 0 | yes |
| 13 | `//enterprise/server/util/oci:oci_test` | L | 72.06 | 72.06 | 1 | small / 600 | — | 0/5 | 0 | no |
| 14 | `//enterprise/server/test/webdriver/saml:saml_test_chromium-local` | L | 64.06 | 64.06 | 1 | large / 900 | docker,webdriver,native | 0/5 | 0 | yes |
| 15 | `//enterprise/server/raft/client:client_test` | L | 55.66 | 55.66 | 1 | medium / 600 | — | 0/5 | 0 | yes |
| 16 | `//enterprise/server/execution_service:execution_service_test` | L | 51.53 | 51.53 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 17 | `//enterprise/tools/metronome_exporter:metronome_exporter_test` | L | 46.23 | 46.23 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 18 | `//enterprise/server/execution_search_service:execution_search_service_test` | L | 45.95 | 45.95 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 19 | `//enterprise/server/backends/userdb:userdb_test_postgres` | L | 45.23 | 177.42 | 5 | medium / 600 | docker | 0/5 | 0 | yes |
| 20 | `//enterprise/server/raft/sender:sender_test` | L | 44.65 | 44.65 | 1 | medium / 600 | block-network | 0/5 | 0 | yes |
| 21 | `//enterprise/server/test/integration/workflow:workflow_test` | L | 42.88 | 140.59 | 6 | medium / 600 | block-network | 0/5 | 0 | yes |
| 22 | `//enterprise/server/backends/distributed:distributed_test` | L | 42.59 | 42.59 | 1 | small / 120 | block-network | 0/5 | 0 | no |
| 23 | `//enterprise/server/test/integration/remote_bazel:remote_bazel_test` | L | 42.16 | 141.30 | 7 | medium / 600 | block-network | 5/5 | 0 | yes |
| 24 | `//cli/test/integration/cli:cli_test` | L | 38.64 | 120.16 | 4 | medium / 600 | — | 5/5 | 0 | yes |
| 25 | `//enterprise/server/backends/authdb:authdb_test_mysql` | L | 37.31 | 37.31 | 1 | medium / 600 | docker | 0/5 | 0 | yes |
| 26 | `//enterprise/server/backends/authdb:authdb_test_postgres` | L | 36.82 | 36.82 | 1 | medium / 600 | docker | 0/5 | 0 | yes |
| 27 | `//enterprise/server/oci/ocifetcher:ocifetcher_test` | L | 36.67 | 36.67 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 28 | `//enterprise/server/usage:usage_test_mysql` | L | 29.61 | 195.76 | 7 | medium / 600 | docker | 0/5 | 0 | yes |
| 29 | `//enterprise/server/test/performance/bytestream:bytestream_test` | P | 29.37 | 29.37 | 1 | medium / 900 | performance | 5/5 | 0 | no |
| 30 | `//enterprise/server/scheduling/scheduler_server:scheduler_server_test` | L | 28.88 | 28.88 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 31 | `//enterprise/server/test/integration/ci_runner:ci_runner_test` | L | 27.95 | 355.99 | 31 | medium / 600 | block-network | 0/5 | 0 | yes |
| 32 | `//enterprise/server/test/webdriver/invocation:invocation_test_chromium-local` | L | 27.88 | 82.23 | 3 | large / 900 | webdriver,native | 0/5 | 0 | yes |
| 33 | `//enterprise/server/test/integration/cli/stream_run_logs:stream_run_logs_test` | L | 27.35 | 27.35 | 1 | medium / 600 | — | 5/5 | 0 | yes |
| 34 | `//server/remote_cache/byte_stream_server:byte_stream_server_test` | L | 26.62 | 26.62 | 1 | small / 120 | — | 0/5 | 0 | no |
| 35 | `//server/util/proto:proto_test` | L | 25.79 | 25.79 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 36 | `//enterprise/server/byte_stream_server_proxy:byte_stream_server_proxy_test` | L | 24.79 | 36.66 | 2 | medium / 600 | — | 0/5 | 0 | no |
| 37 | `//enterprise/server/remote_execution/containers/docker:docker_test` | L | 24.74 | 46.59 | 2 | medium / 600 | docker | 0/5 | 0 | yes |
| 38 | `//enterprise/server/auditlog:auditlog_test` | L | 24.35 | 24.35 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 39 | `//server/remote_cache/content_addressable_storage_server:content_addressable_storage_server_test` | L | 22.61 | 73.31 | 6 | small / 120 | — | 0/5 | 0 | no |
| 40 | `//enterprise/server/remote_execution/copy_on_write:copy_on_write_test` | L | 22.42 | 22.42 | 1 | medium / 600 | — | 0/5 | 0 | no |


### Top 40 by total test time

| Rank | Target | Job | Wall s | Total s | Shards | Size / timeout s | Tags | Fresh runs / n | Retries | Integration |
|---:|---|---|---:|---:|---:|---|---|---|---:|---|
| 1 | `//enterprise/server/remote_execution/containers/firecracker:firecracker_test` | B | 195.53 | 1404.91 | 30 | medium / 900 | bare | 0/5 | 0 | yes |
| 2 | `//enterprise/server/raft/store:store_test` | L | 160.07 | 868.08 | 8 | medium / 600 | block-network | 0/5 | 0 | yes |
| 3 | `//enterprise/server/test/integration/remote_execution:remote_execution_test` | L | 84.98 | 625.72 | 11 | enormous / 900 | cpu:8 | 0/5 | 0 | yes |
| 4 | `//enterprise/server/test/performance/cache:cache_test` | P | 580.75 | 580.75 | 1 | medium / 900 | performance | 5/5 | 0 | no |
| 5 | `//enterprise/server/raft/txn:txn_test` | L | 200.65 | 487.13 | 8 | medium / 600 | block-network | 0/5 | 0 | yes |
| 6 | `//enterprise/server/test/integration/ci_runner:ci_runner_test` | L | 27.95 | 355.99 | 31 | medium / 600 | block-network | 0/5 | 0 | yes |
| 7 | `//enterprise/server/backends/userdb:userdb_test_mysql` | L | 74.86 | 278.22 | 5 | medium / 600 | docker | 0/5 | 0 | yes |
| 8 | `//enterprise/server/remote_execution/containers/ociruntime:ociruntime_test` | L | 106.06 | 213.44 | 4 | medium / 600 | docker | 0/5 | 0 | yes |
| 9 | `//enterprise/server/usage:usage_test_mysql` | L | 29.61 | 195.76 | 7 | medium / 600 | docker | 0/5 | 0 | yes |
| 10 | `//enterprise/server/backends/userdb:userdb_test_postgres` | L | 45.23 | 177.42 | 5 | medium / 600 | docker | 0/5 | 0 | yes |
| 11 | `//enterprise/server/test/webdriver/executions_clickhouse:executions_clickhouse_test_chromium-local` | L | 78.05 | 153.44 | 2 | large / 900 | webdriver,native | 0/5 | 0 | yes |
| 12 | `//enterprise/server/test/integration/remote_bazel:remote_bazel_test` | L | 42.16 | 141.30 | 7 | medium / 600 | block-network | 5/5 | 0 | yes |
| 13 | `//enterprise/server/test/integration/workflow:workflow_test` | L | 42.88 | 140.59 | 6 | medium / 600 | block-network | 0/5 | 0 | yes |
| 14 | `//enterprise/server/raft/metadata:metadata_test` | L | 136.42 | 136.42 | 1 | medium / 600 | — | 0/5 | 0 | yes |
| 15 | `//codesearch/index:index_test` | L | 120.43 | 120.43 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 16 | `//cli/test/integration/cli:cli_test` | L | 38.64 | 120.16 | 4 | medium / 600 | — | 5/5 | 0 | yes |
| 17 | `//enterprise/server/test/integration/remote_execution/bazel_rbe:bazel_rbe_test` | L | 8.96 | 114.19 | 15 | medium / 600 | — | 0/5 | 0 | yes |
| 18 | `//enterprise/server/usage:usage_test_postgres` | L | 19.70 | 113.45 | 7 | medium / 600 | docker | 0/5 | 0 | yes |
| 19 | `//enterprise/server/test/integration/remote_cache:remote_cache_test` | L | 13.87 | 93.75 | 8 | small / 120 | block-network | 0/5 | 0 | yes |
| 20 | `//enterprise/server/test/webdriver/invocation:invocation_test_chromium-local` | L | 27.88 | 82.23 | 3 | large / 900 | webdriver,native | 0/5 | 0 | yes |
| 21 | `//enterprise/server/test/integration/podman:podman_test` | L | 81.51 | 81.51 | 1 | large / 600 | block-network,docker | 0/5 | 0 | yes |
| 22 | `//enterprise/server/backends/pebble_cache:pebble_cache_test` | L | 78.90 | 78.90 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 23 | `//server/remote_cache/content_addressable_storage_server:content_addressable_storage_server_test` | L | 22.61 | 73.31 | 6 | small / 120 | — | 0/5 | 0 | no |
| 24 | `//enterprise/server/util/oci:oci_test` | L | 72.06 | 72.06 | 1 | small / 600 | — | 0/5 | 0 | no |
| 25 | `//enterprise/server/test/webdriver/saml:saml_test_chromium-local` | L | 64.06 | 64.06 | 1 | large / 900 | docker,webdriver,native | 0/5 | 0 | yes |
| 26 | `//server/test/integration/build_event_protocol:build_event_protocol_test` | L | 10.55 | 59.15 | 6 | enormous / 900 | — | 0/5 | 0 | yes |
| 27 | `//enterprise/server/raft/client:client_test` | L | 55.66 | 55.66 | 1 | medium / 600 | — | 0/5 | 0 | yes |
| 28 | `//enterprise/server/usagealert:usagealert_test` | L | 18.81 | 52.81 | 4 | medium / 600 | — | 0/5 | 0 | no |
| 29 | `//enterprise/server/execution_service:execution_service_test` | L | 51.53 | 51.53 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 30 | `//enterprise/server/workflow/service:service_test` | L | 10.18 | 47.09 | 6 | small / 120 | — | 0/5 | 0 | no |
| 31 | `//enterprise/server/test/webdriver/cli_login:cli_login_test_chromium-local` | L | 15.90 | 47.02 | 3 | large / 900 | webdriver,native | 5/5 | 0 | yes |
| 32 | `//enterprise/server/remote_execution/containers/docker:docker_test` | L | 24.74 | 46.59 | 2 | medium / 600 | docker | 0/5 | 0 | yes |
| 33 | `//enterprise/tools/metronome_exporter:metronome_exporter_test` | L | 46.23 | 46.23 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 34 | `//enterprise/server/execution_search_service:execution_search_service_test` | L | 45.95 | 45.95 | 1 | medium / 600 | — | 0/5 | 0 | no |
| 35 | `//enterprise/server/raft/sender:sender_test` | L | 44.65 | 44.65 | 1 | medium / 600 | block-network | 0/5 | 0 | yes |
| 36 | `//enterprise/server/backends/distributed:distributed_test` | L | 42.59 | 42.59 | 1 | small / 120 | block-network | 0/5 | 0 | no |
| 37 | `//server/test/webdriver/invocation:invocation_test_chromium-local` | L | 19.74 | 38.70 | 2 | small / 120 | webdriver,native | 0/5 | 0 | yes |
| 38 | `//enterprise/server/backends/authdb:authdb_test_mysql` | L | 37.31 | 37.31 | 1 | medium / 600 | docker | 0/5 | 0 | yes |
| 39 | `//enterprise/server/backends/authdb:authdb_test_postgres` | L | 36.82 | 36.82 | 1 | medium / 600 | docker | 0/5 | 0 | yes |
| 40 | `//enterprise/server/oci/ocifetcher:ocifetcher_test` | L | 36.67 | 36.67 | 1 | medium / 600 | — | 0/5 | 0 | no |


### Structural findings

1. **Oversized and over-timeout tests:** `ociregistry_client_test` is large, **5.137 s**, moderate timeout **600 s** under race (~117× runtime). `commandutil_test` is enormous, **14.49 s**, default eternal timeout **3600 s** (~248×). `build_event_protocol_test` is enormous / explicit long, median target envelope **10.656 s**, **59.151 s aggregate** across six shards, timeout **900 s** (~84× envelope). These are historical cache timings; demand fresh runs and tail/headroom evidence before reducing limits. Reclassifying size does **not** speed up passing tests. Sources: E20 and per-target CSV.
2. **Large unsharded execution, not necessarily large size:** metadata/index/pebble/client/sender have one shard, despite the runtimes above. Medium-size metadata has **600 s** race timeout versus **136.42 s** measured history, but this alone does not establish a safe smaller timeout. Separately, remote execution is already **enormous / long / 11 shards**, ci_runner **medium / 31**, bazel_rbe **medium / 15**, remote_bazel **medium / 7**, workflow **medium / 6**, remote_cache **small / 8**. Avoid the mistaken backlog item “shard the big integration tests”: most are already sharded. Source E02/E03/E07/E09/E15 plus BUILD metadata.
3. **Within-target serial work remains:** nested txn retry matrices and OCI networking cases execute serially within one top-level test, so more shards alone may not distribute their cases. OCI's latest XML `TestNetworking` is **96.29 s**, including an individual **21.96 s** enabled-network case. The main OCI target median maximum is **106.06 s**, total **213.44 s**. Promote isolated scenarios to top-level tests while retaining actual container networking. Sources E05/E06 + XML.
4. **Timeout / sleep hygiene:** pebble cache has two **3 s** sleeps around negative eviction assertions. An early `Eventually(no eviction)` would weaken those assertions. Wait for a completed janitor sweep and then check retained entries. Podman Run/Exec timeout tests each use **5 s** deadlines; shorten only with a handshake proving the child is running before starting the shorter cancellation window. Notification PubSub uses a **3 s** async-subscribe sleep; query subscription readiness instead. Sources E11–E13/E18.
5. **Flake cost observed: 0 s of retried attempts in this sample.** `ci-shared` allows **2 attempts**, GitHub arm64 explicitly sets **3**. That is a configured policy, not proof of flakiness. Cached passing outcomes and successful-run selection understate the wider failure rate. No target is labeled flaky from this sample, and changing retry policy is rejected. Source E01, `.github/workflows/test-executor-linux-arm64.yaml`, observations.
6. **No demonstrated serial-job opportunity:** GitHub main has one build job and sequential checkout/cache/Test/Slack setup; those setup steps feed Test. BuildBuddy actions have independent one-step test jobs. `allow_concurrent_runs: false` controls cancellation of older runs, and the default branch is allowed concurrently by default; it does not serialize the Test / Baremetal / Benchmark jobs. Sources E21/E24. No “parallelize serial CI jobs” saving is claimed.
7. **Tags / suite semantics:** `tests-ci=-performance,-bare`; `tests-remote=-performance`; local defaults also exclude Docker, bare and secrets. Benchmark uses `+performance`, Baremetal `+bare`, arm64 `-performance,-bare,-docker`; the last suite filter replaces earlier filters. Do not remove Docker/real-DB tests or change these selectors to obtain a speedup. A tag `bare` selects special tests; many ordinary tests request Firecracker isolation through execution properties without being tagged `bare`. Sources E01/E23.

## 3. Candidate tasks

The rows below are ranked by estimated saving per implementation effort, with stronger code evidence favored over speculative setup reuse. **Ranges starting at zero are modeled bounds, not a confidence interval or a measured improvement.** Wall saving means affected target wall time when that target executes; all cached-only rows save zero in the sampled warm suite. Total means summed test attempts. Do not sum rows into a job-time promise; shard reassignment, longest remaining cases, cache invalidation and available RBE capacity affect the result.

Files below are exact ownership boundaries; the machine-readable [candidate manifest](survey-data/candidates.json) is checked for overlaps. There are **19 rows / no shared filenames**. Row `rbe-registration-poll` touches shared harness infrastructure and must be **serialized** with any other harness work. Package-local BUILD files are independent; all changes to `.bazelrc`, shared `.bazelrc`, CI config, `testutil`, or harness packages outside this manifest must enter a serialized lane. Source evidence IDs link to [source-evidence.md](survey-data/source-evidence.md), which contains each command and its output.

Verification notation: **V** = compare baseline and changed revisions with identical test/race settings and `--nocache_test_results`, collect per-shard/attempt BEP, and compare medians across ≥5 successful runs. Use the same VM for any local before/after; report local numbers separately. Repeat scheduling/readiness changes under `--runs_per_test=20`, ensure the same test/scenario inventory and assertions, and inspect failures without hiding retries. **S** = V plus verify the union of test names across shards matches the baseline exactly; inspect longest case, shard balance, empty shards, and total setup overhead. These are verification commands for future implementation, not experiments claimed as completed here.


| slug | what to do | files/dirs | expected saving (s, which tests) | coverage argument (why nothing is lost) | risk (flakiness!) | how to verify |
|---|---|---|---|---|---|---|
| controlled-action-merging | Replace sleep-60 task with an explicitly released controlled command; keep >250ms renewal boundary. Also expose both custom-resource variants as separate top-level tests. E02 + XML. | `enterprise/server/test/integration/remote_execution/remote_execution_test.go` | ~59.65 total for LongTask; latest retained target wall upper saving 19.696 from that change alone; 0 in sampled warm runs | Same action digest, both accepted operation IDs equal after TTL; task genuinely remains live; retain both measured/unmeasured resource cases and all resource-routing assertions | Medium: accidental cache hit or releasing before renewed lease would lose the scenario | V/S; LongTask and both resources cases repeated; verify real task starts, waits beyond TTL, merges, then finishes |
| metadata-four-shards | Add shard_count=4 to 20 top-level metadata tests. E03. | `enterprise/server/raft/metadata/BUILD` | 0–102.31 wall model (136.42×3/4); total unchanged or higher; cold only | Every existing metadata and GCS-atime test still executes once, with isolated processes | Medium: unbalanced LRU case, extra cluster/RBE capacity | S; 1→4, retain all 20 names including GCS cases |
| index-four-shards | Add shard_count=4; 30 DB-owning top-level tests. E04. | `codesearch/index/BUILD` | 0–90.32 wall model (120.43×3/4); total unchanged or higher; cold only | Keep all query, compaction, TF payload and DB-format assertions; no dataset reduction | Low/medium: longest individual format test can limit gain | S; inspect per-case times and shard completeness |
| txn-shardable-fault-matrix | Extract the 12 split retry scenarios into independent top-level wrappers around an unchanged scenario helper; keep 8 existing shards. E05. | `enterprise/server/raft/txn/txn_test.go` | 0–129.36 wall opportunity (latest 200.651 vs next 71.295 shard); total not reduced; cold only | Retain every range × prepare/commit × before/after-apply fault, fired assertion, range records and cleanup; fresh cluster per scenario | Medium/high: helper extraction, global flags, changed shard assignment | S; compare 12 fault tuples and fired/result assertions; retain other retry matrices |
| oci-shardable-network-matrix | Expose the six real-network scenarios as top-level tests with shared parameter helper, keeping per-case isolated resources. E06. | `enterprise/server/remote_execution/containers/ociruntime/ociruntime_test.go` | 0–72.22 wall theoretical ceiling (106.06−213.44/4); total unchanged or higher; cold only | Keep loopback, external connectivity, bytes sent/received, bridge/off/override/userspace paths and real container networking | High: process-global flags mean avoid t.Parallel; shard processes instead | S; all six modes, real network-enabled RBE image, same network stats checks |
| hosted-bazel-shards-and-polls | Increase 7→14 shards to separate EmbeddedBinariesFromApp from BuildRemotelyRunLocally; cap helper polling at 100ms with elapsed deadline instead of ever-growing delays. E15 + XML. | `enterprise/server/test/integration/remote_bazel/BUILD`<br>`enterprise/server/test/integration/remote_bazel/remote_bazel_test.go` | 0–13.04 wall from splitting latest critical shard; polling 0–15 total hypothesis across this target; measured median wall 42.163 | Retain embedded runner/CLI upload and missing-before/present-after CAS assertions; retain invocation/status/log readiness conditions and full failure deadline | Medium: new shard mapping may not isolate desired cases; higher poll load; cap must not shorten total budget | V/S; test all hosted-Bazel cases; report last fresh finisher and full job wall |
| cli-eight-shards | Increase shard_count 4→8 for separate CLI workspace cases. E08. | `cli/test/integration/cli/BUILD` | 0–19.32 target wall ideal (38.637/2); total unchanged or higher; always executes here | All CLI plugin/bazelrc/Bazelisk scenarios still run once; no invocation type is faked | Medium: subprocess/sidecar setup overhead; largest case remains serial | S; inventory all top-level CLI tests and nested cases, inspect sidecar isolation |
| stream-logs-five-shards | Add shard_count=5 for five top-level test groups. E07. | `enterprise/server/test/integration/cli/stream_run_logs/BUILD` | 0–21.88 target wall ideal (27.35×4/5); total unchanged or higher; always executes here | Keep real build, failing command, multi-chunk flushing, upload-failure policies, and signal forwarding | Medium: upload-failure group remains nested; more servers/CLI processes | S; require all failure modes, flush chunks and SIGTERM assertions |
| store-sixteen-shards | Try shard_count 8→16 across 41 top-level store tests; accept only if overhead-adjusted wall improves. E09. | `enterprise/server/raft/store/BUILD` | 0–80.03 target wall ideal (160.066/2); total unchanged or higher; cold only | No raft leader/lease/range/replication scenario removed; each test remains isolated | Medium/high: Firecracker/compute capacity, slower elections under oversubscription | S; capture queue/provision time as well as test duration |
| client-four-shards | Shard the four independent raft client top-level tests into four processes. E10. | `enterprise/server/raft/client/BUILD` | 0–41.75 target wall ideal (55.66×3/4); total unchanged or higher; cold only | Retain session, concurrent session, refresh and error-code cases with fresh replicas | Medium: one parallel-session case could dominate | S; real Dragonboat tests, same proposal counts/assertions |
| sender-four-shards | Add shard_count=4 to the seven raft sender tests; retain per-test global flag isolation through processes. E10. | `enterprise/server/raft/sender/BUILD` | 0–33.48 target wall ideal (44.645×3/4); total unchanged or higher; cold only | Keep lookup, partition and retry/idempotence assertions; no reuse of mutable range state | Medium: flags prevent naive in-process parallelism; setup overhead | S; all seven test names, repeated retries/range operations |
| pebble-completed-sweep | Add a test observation hook for completed janitor sweeps; replace both negative-eviction 3s waits with wait-for-completed-sweep before asserting every entry retained. E11/E12. | `enterprise/server/backends/pebble_cache/pebble_cache.go`<br>`enterprise/server/backends/pebble_cache/pebble_cache_test.go` | 0–6 total and target wall in 78.90s pebble_cache_test; cold only | Observe the real janitor attempting work; preserve min-age and high-threshold negative checks and subsequent positive eviction | High: a pre-sweep or idle signal would weaken coverage; production hook should be inert by default | V; force a sweep that would evict if threshold/min-age logic were broken; same entry checks |
| podman-deadline-handshake | For Run/Exec timeout tests, wait for child-start output, then test a shorter deadline/cancellation interval with existing termination/stat assertions. E13. | `enterprise/server/test/integration/podman/podman_test.go` | 0–8 total and target wall model (two 5s windows→1s); target historical 81.51s; cold only | Both Run and Exec still exercise real deadline propagation, termination and recorded stats; preserve timeout outcome | High: context must be the actual container execution context; retain startup headroom | V; both deadline paths, verify child started and dies on deadline; Docker/Podman-capable RBE |
| workflow-capped-polling | Cap 50ms-doubling waits at 100ms using elapsed deadlines, including child completion checks. E14. | `enterprise/server/test/integration/workflow/workflow_test.go` | 0–10 total hypothesis in 140.59s total / 42.88s wall workflow_test; cold only | Same created/status/child conditions, same cancellation scenarios and complete elapsed timeout budget | Medium: increased RPC load; deadline conversion must preserve tail headroom | V; cancellations and multi-workflow cases repeated; same request-context/auth checks |
| compact-parallel-repetitions | Parallelize independent compact-log fixture/repetition subtests in isolated workspaces or read-only workspace use; retain all three repetitions and both sort modes. E16. | `cli/test/integration/compact/compact_test.go` | 0–9 target wall model at parallelism 2 versus 18.115s; total not reduced; always executes here | Same fixture golden JSON, sorted and unsorted outputs, three executions checking determinism | Medium: CLI shared env/cache initialization; cap concurrency to avoid noisy tests | V; same fixture × 2 sort × 3 repetition inventory and exact golden comparisons |
| singleflight-follower-barrier | Replace the two 2s callback sleeps with a callback release barrier after a test Redis hook observes all followers entering result-wait/claim processing. E17. | `enterprise/server/util/dsingleflight/dsingleflight_test.go` | 0–4 total and target wall in 6.359s dsingleflight_test; cold only | Retain 20 callers, exactly one execution, all same-result checks and error propagation using real Redis | Medium/high: merely starting goroutines is insufficient; prove followers reached distributed coordination | V; delay winner until follower hook condition; repeat success/error concurrency cases |
| notification-subscribe-readiness | Wait for Redis PUBSUB NUMSUB(serverNotifications/app)=2, then publish the existing single message; replace 3s sleep. E18 plus server_notification.go channel definition. | `enterprise/server/server_notification/server_notification_test.go` | 0–3 total and target wall in 5.34s notification test; cold only | Both actual subscribers must be registered before publish; still require both receive the identical protobuf message | Low/medium: channel name coupling and leaked subscriptions; use bounded readiness wait | V; repeat startup races; never use repeated publishing as the readiness condition |
| rbe-registration-poll | Reduce registration-set polling from 100ms to 10ms, preserving timeout and exact executor-set equality. E19. SHARED HARNESS—SERIALIZE. | `enterprise/server/test/integration/remote_execution/rbetest/rbetest.go` | 0–0.09 wall/total per satisfied registration wait; number of waits must be measured; affects RBE integration targets | Same exact expected executor set and deadline; no early acceptance of partial registration | Medium: RPC/Redis load under many executors; shared harness affects many tests | V; all harness consumers; count polls, compare load and failures before accepting |
| commandutil-size-timeout | Replace enormous/default-eternal with an appropriate explicit timeout after fresh cross-platform tail measurements; adjust size only when all platform cases fit. E20. | `enterprise/server/remote_execution/commandutil/BUILD` | 0 passing-test saving; potential failure bound 3600→600 saves up to 3000s per failed attempt, conditional on validation | Retain Linux/macOS/Windows process-tree, signal, cancellation and stats cases; no skips | High: one race-enabled/Linux duration does not bound Windows tails | V across all three OSes, stress process-tree tests; retain ample deadline margin |

Example future verification command (on an authorized CI/RBE runner; do not reuse any service credential from this survey):

```bash
bazel test --config=linux-workflows --config=race --config=tests-ci \
  --nocache_test_results --runs_per_test=20 \
  //enterprise/server/test/integration/remote_bazel:remote_bazel_test
# For a local-capable test on this VM, use --config=local, same flags on both revisions,
# and keep local timings separate. Docker and bare suite filters require explicit selection.
```

No implementation experiments were run: this is a CI-data survey, with no measured before/after improvements. Environment probes returned `nproc=2`, Docker server `29.1.3`, and opening `/dev/kvm` as the current user failed with `Errno 13: Permission denied`. Docker is available; do not blanket-claim Docker tests cannot run. KVM-dependent Firecracker tests cannot run as this user without changing host permissions. Cloud/secret-dependent cases have not been locally provisioned or executed. There is no need to slow-build hundreds of targets to reproduce already-accessible upstream timings. These constraints affect future local validation, not the CI measurements above.

## 4. Rejected or deferred

| Lead | Evidence / numbers | Why rejected or deferred |
|---|---|---|
| Delete, skip, mark manual, filter out slow integration/Docker/bare tests | E01/E23; filters explicitly separate suites | Removes scenarios or real-dependency coverage; forbidden. |
| Cut the performance cache benchmark run length | P: median 580.746s test, 598.607s invocation; 5 fresh runs | Changes measurement quality/workload; benchmark profiling required before a safe speed task. |
| Call cached test durations current CI spend | B: 1404.914s historical total, 3.092s current median invocation; all 30 shards cached | Cached execution durations are inherited. Savings apply after relevant cache invalidation, not to these warm runs. |
| Treat BB Test timing as GitHub CI timing | GitHub Test step median 207s; BB Test invocation median 54.715s; E01/E24 | Different race flags and runners; no GitHub per-target logs. Cause of the difference is unmeasured. |
| Reduce flaky_test_attempts or name a flaky target | 0 retry actions / no FLAKY groups in 20 successful invocations; config allows 2 attempts | Policy is not measured flakiness; successful/cached sample is biased. Preserve retry policy. |
| Parallelize supposedly serial CI jobs | E21/E24; cancellation semantics, default master exemption | No serialized independent jobs demonstrated. Checkout/cache/Test setup dependencies are real. |
| Shorten oomkiller's two 10s sleeps | E22: sleeps inside synctest.Test with synctest.Wait | Simulated time, not 20s wall cost. Removing them would reduce idle-poll scenario coverage. |
| Replace pebble no-eviction sleeps with Eventually(all entries exist) | E11/E12: two 3s waits and every-entry assertions | First poll can pass before janitor ever runs. Use a completed real sweep instead. |
| Remove compact-log repeated executions as duplicates | E16: sorted/unsorted fixtures, each run 3 times; ~18.115s target | Repetitions check determinism, not proven duplicate coverage. Parallelize while retaining them. |
| Share one mutable raft cluster across all txn/store cases | E05/E09: faults, range mutation, elections, cleanup; 487.13/868.08s totals | Isolation and failure-state coverage would be weakened without demonstrated full reset. Promote independent cases / shard first. |
| Blind t.Parallel on workflow/OCI/raft/remote execution | E03/E06/E09/E10; flags.Set and resource/port state | Global flags and shared services can create races. Separate shard processes or independently prove isolation. |
| Replace real OCI networking with a fake ping or fake runtime | E06/XML: networking test 96.29s, checks actual connectivity and byte accounting | Real isolation/connectivity is the code path under test. Keep actual network scenarios; distribute cases. |
| Globally shorten shared.bazelrc test deadlines | E01: race uses 120/600/900/3600s | No passing-test speedup, cross-platform tail/flakiness risk. Target-local deadline hygiene remains a low-ranked candidate. |
| Add more Firecracker shards immediately | B: already 30 shards, ~195.53s max / 1404.91s total, fully cached | Need uncached per-case profile and capacity evidence; more shards may just add VM provisioning. KVM unavailable to this VM user. |
| Cache nested Bazel mutable outputs globally / add remote execution locally | E25 and testbazel.go: prewarmed vendor/repository/install inputs already exist | Test launcher already avoids downloads; shared mutable output bases can leak state or lock-contend. Not a demonstrated new saving. |
| Proven duplicate removal | Case XML and source assertions reviewed | No concrete pair with identical code paths and assertions was established. No deletion task proposed. |

Remaining research: obtain authorized GitHub Actions logs if exact GitHub-only per-target medians are required; obtain ≥5 **uncached executions per prioritized cold target**, not five appearances of one cached result; use execution-graph timing to attribute full critical paths and provisioning/queue overhead; collect macOS/Windows timings before changing shared timeout or resource policy. No credential or permission change was made for this survey.

