# Test-time survey

Research only; checkout `770b88bc89c0b20059d42a237e8c7923ff5b7215`. All estimates below are **source-derived**, per uncached test invocation, unless explicitly marked measured. Public CI job durations are recorded below; per-test CI logs were unavailable (HTTP 403 / no CLI authentication). Per-test savings are source-derived or measured locally, not measured CI savings. Wall-time savings refer to the affected test target, not automatically the whole CI critical path. Total test time is the sum of test durations; overlapping tests and parallel cases make it different from wall time.

## Summary

**Locally measured LRU win:** the approximate-LRU tests fell from 12.402s to .394s across three repetitions (**4.003s saved per two-test run**, total test time and serial target wall opportunity), retaining all assertions. This was a 10ms real ticker / 1ms condition-poll overlay, not the final fake-clock design. Three repetitions establish feasibility, not a flakiness certification.

**Measured Raft opportunity:** the ErrorCodes table takes about **18s** per run for six temporary errors with 3s deadlines. Running each subtest in synctest reduced the selected-body target from **36.3s to .2s across two repetitions**, preserving all 12 cases and assertions. The full client suite still needs PR verification. Dependency classification is recorded in [the pinned Dragonboat source excerpt](survey-evidence/dragonboat-temp-errors.txt).

**Measured disk-cache opportunity:** seven timestamp waits fell from **22.2s to 1.6s across three repetitions**, saving about **6.87s per invocation** with 20ms waits. All 21 size scenarios passed. A 1ms experiment failed the strict atime assertion, so the proposed fix requires filesystem timestamp readiness. Together, Raft, disk and LRU show roughly **29s of summed test-time opportunity** per selected invocation of each; these separate experiments do not establish an aggregate CI wall saving.

Other candidates include disk-cache timestamp waits (about **7 seconds**), a proven duplicate Podman Run timeout test (**at least 5 seconds**, plus duplicate setup), Pebble janitor acknowledgements (up to **6.5 seconds**), distributed singleflight barriers (up to **4 seconds**), and Redis PubSub readiness (**about 3 seconds**). These contain 25.5 seconds of deliberately elapsed time; some synchronization work remains, so 25.5 seconds is an opportunity ceiling, not a measured speedup. The changes preserve timestamp comparisons, deadline errors and debug output, negative eviction assertions, all 20 singleflight callers, and both PubSub receivers.

The largest integration opportunity is capped polling. Workflow has **26 static helper call sites**, with exponential sleeps from 50ms through **51.2s**. A successful invocation detected after a 3.2s sleep can lose up to 3.1s relative to a 100ms cap. If this happened at all 26 sites, the target would save up to **80.6s**; this is a conditional scenario, not an observed CI number. Hosted Bazel uses the same pattern at three static call sites. Test target savings equal total test-time savings while these cases remain serial. Overall CI wall savings require critical-path measurements; they can be zero if another target dominates.

Do **not** charge the OOM killer's 10s sleeps: they run under `synctest`. Do not shorten size-threshold tests blindly or parallelize tests that mutate package flags. A smaller poll interval alone does not fix the approximate-LRU's production **1s** eviction ticker.

## Measurements and evidence

Full inventories are included, rather than treating every timeout declaration as a cost:

- [Fixed sleeps](survey-evidence/sleep-inventory.md): **148 sites** across `*_test.go`, Go `testutil` paths and the RBE integration harness; exact duration expressions, enclosing functions, frequency qualifications, suggested treatment. Dynamic helper/loop counts are not invented.
- [Eventually calls](survey-evidence/polling-inventory.md): **110 full calls**, retaining both timeout and poll interval.
- [Raw timing scan](survey-evidence/timing.txt), [helper scan](survey-evidence/helpers.txt), [context deadlines](survey-evidence/deadlines.txt), [large-fixture scan](survey-evidence/fixtures.txt), [production timing declarations](survey-evidence/production-timing.txt), [existing fake-time use](survey-evidence/fake-time.txt), [TS timing](survey-evidence/ts-timing.txt).
- [Selected source with line numbers](survey-evidence/source-context.txt) and [test / caller counts](survey-evidence/counts.txt) substantiate candidate estimates. File line numbers refer to the checkout above.

Commands used (raw scan outputs are linked above):

```sh
rg -n 'time\.Sleep|clockwork|Eventually|time\.After\(' --glob '*_test.go' --glob '*test*.ts'
# 668 matching lines; this includes imports and fake clocks, not 668 wall-time waits.
rg -n 'time.Sleep|time.NewTicker|time.After|time.Second|time.Minute' server/testutil enterprise/server/testutil
rg -n 'WithTimeout|WithDeadline' --glob '*_test.go'
rg -n 'Random.*(100_|100\*|1024|1e|10_|[0-9]+ <<)|[0-9]+\s*\*\s*(1024|1024\s*\*\s*1024)' --glob '*_test.go'
rg -n 'flag.Duration|time.NewTicker|time.Sleep|time.Second|time.Millisecond|time.Minute' server enterprise --glob '*.go' -g '!**/*_test.go'
rg -n 'clockwork.NewFakeClock|synctest.Test' --glob '*_test.go'
# 206 matches; existing clockwork and synctest support is real.
rg -n 'setTimeout|sleep|fakeTimers|advanceTimers|jasmine.clock' app enterprise/app --glob '*test*.ts'
```

A Python scan of all `*_test.go` and Go files under `testutil` counted 148 `time.Sleep` sites (147 in the initial tests/testutil scope, plus one RBE-harness site) (excluding comment-only lines). A balanced-parentheses scan extracted 110 Eventually calls. Function and helper-reference counts are static source counts, **not runtime occurrence counts**. There are 420 Go test files and 20 `*test*.ts` files plus one actual TSX test (`app/util/react_test.tsx`, no timing calls) in this checkout. See `survey-evidence/reproduce.py` for repeatable counts and arithmetic.

Arithmetic output:

```text
50ms doubling while <60s: .05,.1,.2,.4,.8,1.6,3.2,6.4,12.8,25.6,51.2
sum of possible sleeps per helper failure: 102.35s (not a 60s overall deadline)
26 * (3.2 - .1) = 80.6s conditional workflow detection savings
7 * (1 - .001) = 6.993s disk timestamp opportunity
2 * 3 + 5 * .1 = 6.5s Pebble explicit waits
2 * 2 = 4s singleflight sleeps (winning callbacks, not 40s per case)
.1 * (1+2+4+8+16+32+64+128) = 25.5s webdriver failure sleep ceiling
```

### Local timing procedure

This VM has 2 CPUs and approximately 8GB RAM. Cold dependency download/build time is recorded separately from test runtime. Local Bazel used `--ignore_all_rc_files` to avoid the repository's default BES and remote configuration, `--jobs=2`, and uncached tests. No remote execution was used. The installed `bazel` symlinks to the BuildBuddy CLI, so an official Bazel 9.3.0rc2 binary matching `.bazelversion` was downloaded to `/tmp/test-survey/bazel-real`:

```sh
curl -fL --max-time 60 https://releases.bazel.build/9.3.0/rc2/bazel-9.3.0rc2-linux-x86_64 -o /tmp/test-survey/bazel-real
chmod +x /tmp/test-survey/bazel-real
nohup bash -c '/usr/bin/time -p /tmp/test-survey/bazel-real --ignore_all_rc_files test //server/util/approxlru:approxlru_test //server/backends/disk_cache:disk_cache_test //enterprise/server/util/dsingleflight:dsingleflight_test --jobs=2 --test_output=all --nocache_test_results > /tmp/test-survey/baseline.log 2>&1; echo $? > /tmp/test-survey/baseline.exit' </dev/null > /tmp/test-survey/launch.log 2>&1 & wait
# Poll: tail -25 /tmp/test-survey/baseline.log
nohup bash -c '/usr/bin/time -p go test ./server/util/approxlru -count=3 -v > /tmp/test-survey/go-lru-before.log 2>&1; echo $? > /tmp/test-survey/go-lru-before.exit' </dev/null > /tmp/test-survey/go-launch.log 2>&1 & wait
```

Initial LRU timings: cold Go command real=976.39s, test total=13.197s; per-test durations PartialEviction=1.00,2.00,2.00s and Refresh=2.08,3.00,3.01s. Cold build ran alongside Bazel dependency analysis. A warm before/after comparison is below. A build failure is not a test timing. CI job metadata is available; per-test CI timings remain unavailable.

### Production defaults and helper frequency qualifications

The production-declaration scan is intentionally broader than proven waste. Source inspection traced these defaults; unmeasured periodic callbacks are **not** multiplied by timeout ceilings:

| knob / site | default and runtime frequency | test usage / recommendation |
|---|---|---|
| `server/util/approxlru/approxlru.go:21,374` | 1s ticker per live LRU | Both newCache tests leave real clock; three eviction waits. Clock option exists but this ticker bypasses it; candidate row owns fix. |
| `enterprise/server/hit_tracker_client/hit_tracker_client.go:34` | 250ms periodic flush per live tracker | Ordinary delivery tests use real clock and no update_interval override; fake-clock tests separate. Candidate row proposes local flag only. |
| `server/util/retry/retry.go:96` | 50ms initial, ×2, capped 3s per retry | `raft/client/client.go:244` ErrorCodes cases exercise real default retry until 3s context expires for temporary errors. Six table errors are temporary (dependency IsTempError confirmed); use synctest or parallel cases, keeping retry error aggregation and retry-count coverage. |
| `enterprise/server/scheduling/scheduler_server/scheduler_server.go:66` | 10s lease, renewed during task execution | RBE harness normally uses defaults, but target tests at `remote_execution_test.go:2075,2361` override lease durations to 100ms/50ms. Do not credit 10s to these tests. Ordinary action completion need not wait a whole lease. |
| `enterprise/server/remote_execution/container/container.go:69` | 500ms cgroup stats sampling | Relevant to stats tests; shortening process workloads without ensuring samples weakens stats coverage. Prefer local interval only after per-test sample proof. No seconds credited. |
| `server/backends/disk_cache/disk_cache.go:55,59` | metrics 5s; stale-file age 15min | Metadata/restart tests do not deliberately wait these values. Timestamp comparison uses filesystem stat at :425–438, not a clock option. No 5s/900s credit. |
| `server/testutil/testserver/testserver.go:21,162` | 500ms per unsuccessful ready probe | All binary starts through this helper; count actual starts before calculating aggregate savings. SHARED row. |
| `enterprise/server/testutil/testredis/testredis.go:30,31,259` | 5ms startup ping; 10s failure ceiling | Already fast polling; runtime iterations depend on Redis startup. Reject as low return. |
| `server/testutil/testpostgres/testpostgres.go:105`, `testmysql/testmysql.go:94` | 100ms each failed DB ping | Potential ≤95ms per final detection window with 5ms polls, not per full startup. Both shared helpers; defer pending measured startup counts. |
| `server/testutil/webtester/webtester.go:39,131,581` | optional failure delay defaults 0; API-key fetch 100ms × up to 5 | Don't credit disabled debug delay. API key detection ceiling .5s; low priority shared helper. |

Production clockwork use and declarations alone don't prove time wasted. The comprehensive declaration inventory supports follow-up owners; this survey does not claim a complete dynamic call graph or runtime counts for every production timer.

### Completed local LRU before/after

Commands (each backgrounded with `nohup bash -c '...; echo $? > ...exit' ... & wait`, then logs polled):

```sh
/usr/bin/time -p go test ./server/util/approxlru -count=3 -v
/usr/bin/time -p go test -overlay=/tmp/test-survey/lru-overlay.json ./server/util/approxlru -count=3 -v
```

Overlay changed only `evictCheckPeriod = 1 * time.Second` to `10 * time.Millisecond` and the test helper sleep from 1s to 1ms. All scenarios, entries, samples and assertions were unchanged. [Overlay map](survey-evidence/lru-overlay.json), [production overlay](survey-evidence/approxlru.go.overlay), [test overlay](survey-evidence/approxlru_test.go.overlay); original checkout remained untouched. The map's temporary paths need adjustment if rerunning elsewhere.

| local metric | warm before | overlay after |
|---|---:|---:|
| six test executions, all PASS | 12.402s | .394s |
| full command real | 20.51s | 9.58s |
| full command user | 4.64s | 6.26s |
| full command sys | .81s | 1.50s |
| mean test time per two-test suite | 4.134s | .1313s |

Thus observed test-time saving `(12.402−.394)/3 = 4.0027s` per suite; command saving is 10.93s across three repetitions but includes recompilation/vet. Read [warm before raw output](survey-evidence/go-lru-warm-before.log), [after raw output](survey-evidence/go-lru-after.log), [cold baseline raw output](survey-evidence/go-lru-before.log). Both warm runs shared the 2-CPU VM with the same ongoing cold Bazel build, so contention remains a limitation. Don't extrapolate 4.003s directly to CI; do repeated idle/race runs for the PR. Existing Clock alone does not speed this ticker; recommended implementation preserves production defaults and uses injected time, unlike this temporary feasibility overlay.

### Proven Podman duplicate (source evidence, no local runtime)

Command: Python extracts `TestRun_Timeout` and `TestExec_Timeout` through the next `func` boundary and replaces **only the function name** with `TEST_NAME`; it compares full bytes and SHA256 hashes. See [normalized body and output](survey-evidence/podman-duplicate.txt).

```text
Normalized full functions identical: True
SHA256 Run:  904506829c28dbcb22dcb8548174d8345613ff0d225919a8de1c9d0a15e378b0
SHA256 Exec: 904506829c28dbcb22dcb8548174d8345613ff0d225919a8de1c9d0a15e378b0
Run calls in each: 1
Exec calls in each: 0
```

`rg -n 'TestExec_Timeout|TestRun_Timeout' .github enterprise/server/test/integration/podman --glob '!podman_test.go'` returned no matches (exit 1): no exact-name selection found in these CI configs / package files. Search any external CI test filters before deleting the duplicate.

A local runtime is not claimed: this target's BUILD explicitly configures Firecracker isolation and a root execution image. `testpodman.TestMain` on amd64 installs a static distribution by extracting into `/`; this VM's current UID is 1000 and has no Podman command. Running it here would require privileged system provisioning outside the ordinary local test command. The estimated 5s is the deliberately expired context in the duplicated body, not measured CI data. Shortening the remaining real deadline is a separate, riskier option: process/container startup must still complete stdout/stderr/file writes, so a naive 1s deadline can weaken the scenario through flakiness.

### CI metadata, kept separate from local test durations

Read-only public GitHub API commands:

```sh
curl -sS 'https://api.github.com/repos/buildbuddy-io/buildbuddy/actions/runs?head_sha=770b88bc89c0b20059d42a237e8c7923ff5b7215&per_page=10'
# HTTP 200, total_count=6, six completed successful workflows on 2026-10-09.
curl -sS 'https://api.github.com/repos/buildbuddy-io/buildbuddy/actions/runs/37946229881/jobs?per_page=100'
curl -sS 'https://api.github.com/repos/buildbuddy-io/buildbuddy/actions/runs/37946229925/jobs?per_page=100'
```

Python computed `completed_at - started_at` from [main CI job JSON](survey-evidence/ci-jobs-37946229881.json) and [root-lane job JSON](survey-evidence/ci-jobs-37946229925.json):

| CI workflow | job wall time | test step wall time | timestamps for test step (UTC) |
|---|---:|---:|---|
| [CI](https://github.com/buildbuddy-io/buildbuddy/actions/runs/37946229881) | 274s | 252s | 14:43:16 → 14:47:28 |
| [linux/arm64 root tests](https://github.com/buildbuddy-io/buildbuddy/actions/runs/37946229925) | 164s | 108s | 14:43:47 → 14:45:35 |

These are whole job/step times, including build/setup and possibly cached or parallel tests. They cannot establish individual test time, CI critical path, or CI saved seconds. The root lane corroborates that privileged container tests use a separate execution configuration; no 5s Podman runtime is inferred from its 108s step.

Per-test log attempts:

```sh
curl -L -sS -w '%{http_code}\n' https://api.github.com/repos/buildbuddy-io/buildbuddy/actions/jobs/113873067382/logs
# 403; response message in survey-evidence/ci-job-113873067382.log
curl -L -sS -w '%{http_code}\n' https://api.github.com/repos/buildbuddy-io/buildbuddy/actions/jobs/113873068318/logs
# 403; response message in survey-evidence/ci-job-113873068318.log
gh api --hostname github.com repos/buildbuddy-io/buildbuddy/actions/jobs/113873067382/logs
# exit 4: "To get started with GitHub CLI, please run: gh auth login"
```

No CI test-duration data was fabricated, and no GitHub write or message was sent.

### Completed local cold Bazel baseline

The three-target command above completed: **real=3362.26s (56m02s), exit=3**. [Raw log](survey-evidence/baseline.log) includes the full cold analysis/build and test output. Test target times were:

| original target | local result | elapsed test time |
|---|---|---:|
| disk_cache_test (whole target) | PASS | 14.1s |
| approxlru_test (both tests) | PASS | 5.4s |
| dsingleflight_test (both tests) | FAIL | 8.5s |

Singleflight failure output:

```text
--- FAIL: TestDo (4.09s)
expected: 1
actual: 0
--- FAIL: TestDoError (4.03s)
Error "... could not lock work: redis: connection pool timeout, " does not contain "out of puppies"
```

This is **not a passing speed baseline**. Inspection showed every one of the 20 Do callers starts both `claimWork` and a blocking `XRead` (`dsingleflight.go:188–198`). The pool default is `10 * runtime.GOMAXPROCS(0)` ([dependency source](survey-evidence/redis-pool-default.txt)), while `testredis.Client` uses parsed target options with no explicit capacity. Blocking readers can occupy the CPU-derived pool and prevent lock/renew/result writes. No workers/assertions were removed. The warm paired experiment configures `?pool_size=64` in both TestDo and TestDoError, using the library's existing URL option, then keeps that configuration unchanged before and after the disk/LRU timing changes. Both pool-configured warm runs passed singleflight; observed parent durations were 2.02–2.10s per case. The original failing run is excluded from savings.

A further coverage constraint was found: during the old 2s callback, the **production 1s heartbeat loop exercises Expire renewal**. A callback gate released immediately after caller arrival would drop this path. The candidate explicitly requires a renewal acknowledgement and fake-clock/test-period advancement before release, plus cleanup of canceled claim loops.

### Completed local Raft selected-body before/after

A temporary test target copied **only** the complete original `TestRunNodehostFn_ErrorCodes` body and required imports, linked to the **original** production client. It avoids unrelated replica/store fixture imports; it does not represent a timing of the entire client test suite. All 12 error cases, the 3s context, 1s single-op limit, default retry loop, code assertions and errors.Is assertions are preserved. The after copy adds `synctest.Test` **inside each t.Run**, so real timer code runs under fake time. See [before source](survey-evidence/raft-experiment-test-before.go.txt), [after source](survey-evidence/raft-experiment-test-after.go.txt), [BUILD](survey-evidence/raft-experiment-BUILD.txt), [driver](survey-evidence/raft-experiment.py).

```sh
nohup bash -c 'python3 /tmp/test-survey/raft-experiment.py > /tmp/test-survey/raft-driver.log 2>&1; echo $? > /tmp/test-survey/raft-driver.exit' </dev/null > /tmp/test-survey/raft-launch.log 2>&1 & wait
# driver runs, before and after:
/tmp/test-survey/bazel-real --ignore_all_rc_files test //enterprise/survey_local_experiment:error_codes_test --jobs=2 --nocache_test_results --test_output=all --test_arg=-test.v --test_arg=-test.count=2
```

| local metric | original timing | synctest timing |
|---|---:|---:|
| parent test durations | 18.01s, 18.07s | 0.00s, 0.00s (rounded) |
| target test time, 2 repetitions | 36.3s | .2s |
| full command real | 321.372s | 14.708s |
| test results | 24 subcases PASS | 24 subcases PASS |

Observed mean parent-test saving ≈**18.04s**; target saving `(36.3−.2)/2 = 18.05s` per repetition includes startup and rounding. Before full-command time includes newly compiled client dependencies; don't claim its 306.664s command difference as test savings. [Before raw log](survey-evidence/raft-before.log), [after raw log](survey-evidence/raft-after.log), [driver timing JSON](survey-evidence/raft-results.json).

The initial temporary target was placed outside enterprise and failed package visibility at analysis. It ran **no tests**; [failed attempt](survey-evidence/raft-visibility-attempt.log) is retained and excluded from timing comparisons. The corrected target uses the existing enterprise visibility. All timing commands completed; the temporary package is removed during final cleanup. No production visibility change or replica fixture deletion is proposed.

### Warm disk/LRU pair and filesystem-precision rejection

[Experiment driver](survey-evidence/experiment.py) ran three repetitions of the selected original test bodies. In both phases, singleflight used pool_size=64 with all 20 callers and the original 2s callbacks. No timing change was made to singleflight. The after phase changed LRU's real ticker to 10ms / poll to 1ms and tried disk's timestamp wait at 1ms. [Patch](survey-evidence/experiment.patch), [before log](survey-evidence/warm-before.log), [after log](survey-evidence/warm-after.log), [timing JSON](survey-evidence/experiment-results.json), [source restoration](survey-evidence/experiment-restored.txt).

```sh
nohup bash -c 'python3 /tmp/test-survey/experiment.py > /tmp/test-survey/experiment-driver.log 2>&1; echo $? > /tmp/test-survey/experiment-driver.exit' </dev/null > /tmp/test-survey/experiment-launch.log 2>&1 & wait
# before and after use identical commands:
/tmp/test-survey/bazel-real --ignore_all_rc_files test //server/util/approxlru:approxlru_test //server/backends/disk_cache:disk_cache_test //enterprise/server/util/dsingleflight:dsingleflight_test --jobs=2 --test_output=all --nocache_test_results '--test_arg=-test.run=^(TestMetadata|TestPartialEviction|TestRefresh|TestDo|TestDoError)$' --test_arg=-test.count=3 --test_arg=-test.v
```

| local target, 3 repetitions | before | 1ms disk / accelerated LRU phase |
|---|---:|---:|
| disk TestMetadata | PASS 22.2s | **FAIL .6s; invalid speedup** |
| LRU two-test suite | PASS 13.2s | PASS .7s |
| singleflight two cases, pool64 in both | PASS 12.5s | PASS 12.8s |
| full three-target command | 46.467s, exit 0 | 19.270s, exit 3 |

The warm LRU target difference is `(13.2−.7)/3 ≈ 4.17s` per two-test suite, consistent with the earlier native Go 4.00s observation. Singleflight parent test durations stayed 2.02–2.10s; no speedup is claimed from the unchanged callbacks, and the pool override preserves the concurrency scenario.

The 1ms disk phase failed **all three repetitions** at the existing strict atime assertion, e.g.:

```text
server/backends/disk_cache/disk_cache_test.go:143
"1791853461354741" is not greater than "1791853461354741"
```

A separate, reversible experiment tried **20ms**, retaining all original statements/assertions and all seven sizes. [Driver](survey-evidence/disk20.py), [raw output](survey-evidence/disk-20ms.log), [result JSON](survey-evidence/disk-20ms-results.json), [restoration](survey-evidence/disk-20ms-restored.txt).

```sh
nohup bash -c 'python3 /tmp/test-survey/disk20.py > /tmp/test-survey/disk20-driver.log 2>&1; echo $? > /tmp/test-survey/disk20-driver.exit' </dev/null > /tmp/test-survey/disk20-launch.log 2>&1 & wait
/tmp/test-survey/bazel-real --ignore_all_rc_files test //server/backends/disk_cache:disk_cache_test --jobs=2 --nocache_test_results --test_output=all --test_arg=-test.v '--test_arg=-test.run=^TestMetadata$' --test_arg=-test.count=3
# PASS: target time 1.6s, all 3 repetitions / 21 size scenarios.
```

Observed target saving `(22.2−1.6)/3 ≈ **6.87s**` per TestMetadata run. Before parent durations were 7.37/7.22/7.28s; after parent durations were .41/.40/.42s, and full-command real time was 20.783s (exit 0). These are same-VM observations, not filesystem-portability proof; the 20ms run selected only disk while the earlier baseline selected three targets, so scheduling is a comparison limitation. Do not compare their full-command times as a three-target speedup. The recommended task waits for filesystem timestamp readiness **before a single rewrite**, retaining real stat metadata and strict assertions, rather than assuming 1ms suffices or retrying the rewrite until an assertion passes.

Final cleanup: all tracked source changes and Bazel lockfile changes were restored; the temporary Raft package was removed. Only the research report and evidence remain. [Cleanup commands/output](survey-evidence/final-cleanup.txt).

## Candidate tasks

Ranked by prospective saving per effort, with conditional polling opportunities first. Each row owns **disjoint files**; directories identify location, not permission to edit another row's files. Production timing changes must expose options or clocks without changing production defaults. **SHARED** rows must be serialized, including concurrent verification against tests using their harness. Routine flag-mutating suites must stay serial unless configuration is first made instance-local.

Verification convention: `B= /tmp/test-survey/bazel-real --ignore_all_rc_files`; use `B test //DIR:TARGET --jobs=2 --nocache_test_results --test_output=all --test_arg=-test.run=REGEX --test_arg=-test.count=5`, with the actual target name from that directory's BUILD. Compare medians of individual test durations before/after on this VM; repeat with `--test_arg=-test.count=50` and race instrumentation where supported to assess flakiness. No assertion removal or weaker expected values.

| slug | what to do | files/dirs | expected saving (s, which tests) | coverage argument (why nothing is lost) | risk (flakiness!) | how to verify |
|---|---|---|---|---|---|---|
| workflow-poll-cap | Replace exponential polling with 50–100ms capped intervals and an explicit elapsed deadline; preserve every lookup and terminal-state check. | `enterprise/server/test/integration/workflow/workflow_test.go:324,347,596` | 0–3.1s per call if current completion is detected in a 3.2s sleep; 26 static helper calls gives 0–80.6s conditional target saving, plus child loop. No measured average. | Same webhook, sync/async execution, cancellation, child states and assertions; preserve old effective 102.35s failure budget initially. | Medium: more RPC load; use capped polling rather than tight loop; old `not found` continue branch needs elapsed bound. | Whole workflow target, individual cases, include slow executor startup and cancellation. |
| hosted-bazel-poll-cap | Cap the three hosted invocation/log/status loops at 100ms, retaining chunk pagination and deadline budget. | `enterprise/server/test/integration/remote_bazel/remote_bazel_test.go:101,124,144` | 0–9.3s conditional for three calls detected in 3.2s windows; occasional much larger gains, not measured. | Keep all hosted builds, returned role, log substring and status assertions. | Medium: load and error handling; don't advance log chunks incorrectly. | Whole remote_bazel target; successful and failed invocation cases. |
| raft-error-time | Run each pure ErrorCodes subtest inside synctest.Test; retain all 3s deadlines and real default retry logic. If fake-time isolation is incompatible, parallelize these independent subtests as fallback. | `enterprise/server/raft/client/client_test.go:139,233` | **Measured ~18s**: 6 temporary errors × 3s. Fake time can remove wall and total waiting; parallel fallback on 2 CPUs saves ~9s wall, 0s total. Selected-body target measured 36.3s → .2s across 2 repetitions; parent function 18.01/18.07s → 0.00/0.00s. | All 12 error cases, gRPC codes, errors.Is, retry aggregation and default backoff remain; fake time exercises the same timers without decreasing deadlines/retries. | Low/medium: synctest bubble must be inside t.Run, not around it; global tracing side effects must not block outside bubble. | Selected ErrorCodes before/after, all 12 case names and assertions; fake-time retry count / fake elapsed deadline; race, then full client target. |
| disk-metadata-clock | Use filesystem-precision-aware readiness before the single rewrite (e.g. same-filesystem timestamp probe), retaining strict comparisons; replace restart GC sleep with completion wait. A 20ms local experiment passed; a 1ms one failed. | `server/backends/disk_cache/disk_cache_test.go:135,664` (test-local change; metadata getter reads real filesystem stat) | **Measured ~6.87s** per TestMetadata invocation: 22.2s → 1.6s across 3 repetitions with 20ms waits; 1ms failed. Up to .5s restart-GC wait remains unmeasured. | Preserve all seven sizes and strict access/modify timestamp increases; preserve oldest-item eviction checks. Timestamp result is microseconds; preserve the actual stat conversion and all seven size scenarios, with exactly one post-wait rewrite. | Medium/high: 1ms **failed locally** with unchanged atime. 20ms passed 21 size cases here, not proof for all filesystems. Don’t retry Set until it passes or weaken strict > assertions; wait for timestamp readiness before the one rewrite. | TestMetadata and restart eviction on the VM filesystem, all seven sizes; then whole target. |
| podman-duplicate-timeout | Remove the byte-identical duplicate TestExec_Timeout, retaining TestRun_Timeout unchanged. Both currently call Run. | `enterprise/server/test/integration/podman/podman_test.go:281,335` | **At least 5s source-derived**, plus duplicate provider/image setup, on the Podman lane. No local Podman timing. | Normalized full functions have identical SHA256; same c.Run call, 5s context, DeadlineExceeded / exit code / exact stdout+stderr+file assertions. The retained test covers every current path/assertion. Genuine Exec coverage would be a separate addition. | Low: verify no CI-only exact-name selection; current .github / package scan found none. Removing a unique timeout test would be unacceptable. | Compare normalized bodies/hashes (artifact linked below); run retained test and whole target in its root/Podman lane; compare branch coverage and assertions. |
| pebble-janitor-ack | Replace 3s negative windows and five 100ms sampler wake sleeps with cycle acknowledgements; use existing fake clock and block-until registration. | `enterprise/server/backends/pebble_cache/pebble_cache_test.go:1583,1589,1693`; cycle hook in `pebble_cache.go` | Up to 6.5s across partition min-age / cutoff tests, minus actual cycle processing. | Assert every resource remains before min-age/below cutoff **after janitor has run**, then retain partial eviction checks after eligibility. | Medium/high: absence alone isn't readiness; merely replacing sleeps with Contains would weaken coverage. | PartitionMinEvictionAge / PartitionJanitorCutoffThreshold regex, then all Pebble tests; verify real cycle acknowledgement. |
| dsingleflight-barrier | Replace 2s callbacks with a gate proving all callers entered the flight and at least one owner lease renewal occurred; inject fake clock / short test heartbeat for the production 1s loop. Configure a sufficient test-only Redis pool. | `enterprise/server/util/dsingleflight/dsingleflight_test.go:46,87`, `dsingleflight.go:131`, package `BUILD` for clock dependency | Up to 4s across TestDo / TestDoError, less coordination. Exactly one successful callback per test, 20 callers each. | Preserve execution count=1, all 20 winner/error results and the owner Expire renewal path exercised during the old 2s callback; use real Redis. | Medium/high: early release drops overlap or renewal coverage. Original baseline failed from Redis pool exhaustion on this VM; use explicit pool capacity without reducing workers. | Both cases ×50 plus race; inspect entry/lock attempts and owner Expire before opening gate; ensure fake-clock cancellation cleanup drains claim loops. |
| notification-ready | Wait for both Redis subscription acknowledgements before publishing the single notification. | `enterprise/server/server_notification/server_notification_test.go:26`, `server_notification.go` | Up to 3s minus subscription setup in TestPubSub. | Keep real Redis, exactly one publish, both subscribers' exact proto comparison. | Low/medium: subscription goroutine started is not subscription registered; avoid retry-publish duplicates. | TestPubSub ×50; race; fail if either subscription isn't acknowledged. |
| lru-clock-ticker | Route evictor's 1s real ticker through existing Clock; fake-clock test construction, registration barrier and completion wait. | `server/util/approxlru/approxlru.go:374`, `approxlru_test.go:82`, package `BUILD` (add test clock dependency) | **Measured 4.003s per two-test run** averaged over 3 repetitions (.394s vs 12.402s totals). Three waitForEviction calls; final fake-clock PR may differ. | Keep 1000-entry age distribution, 95% age assertion and all ten refreshed IDs; no change in eviction sampling. | Medium: Clock already exists but ticker ignores it. Fake clock advancement before registration can miss a tick. Reads of c.data need completion synchronization. | Both tests ×50; race; compare same random-size population and eviction assertions. |
| workflow-schedule-ready | Remove 1s sleep followed by Eventually; reduce 100ms poll granularity where callbacks already indicate completion. | `enterprise/server/workflow/service/service_test.go:1767` | ~.99s in TestCreateScheduledWorkflow; additional poll savings depend on actual completions. | Existing Eventually and schedule name/cron/next-run assertions retained; fake 3PM clock unchanged. | Low: maintain outer deadline if background task startup is delayed. Global flags prevent simple t.Parallel. | TestCreateScheduledWorkflow ×50 then all scheduled-workflow cases. |
| pull-concurrency-gate | Replace fake pull's 1s delay with a barrier/observer proving all callers contend before release. | `enterprise/server/remote_execution/container/container_test.go:48,253` | Up to 1s in ParallelCallsSerialized (20 callers, one pull). | Keep all callers and exact PullCount=1; real image pull is not under test here. | Medium: caller launch does not prove entry into keyed lock. Use registration or lock observer, not 20 pre-call signals alone. | ParallelCallsSerialized ×50, race, then whole container target. |
| hit-tracker-flush-option | Set 250ms update interval to 10ms only in ordinary delivery tests; reduce 100ms observation intervals to 5–10ms. | `enterprise/server/hit_tracker_client/hit_tracker_client_test.go:238–431` (line references identify sites; edit this file only) | 12 calls use 100ms Eventually; up to 1.08s observation granularity + .24s per first periodic flush, conditional. | Preserve all counts, anonymous/group separation, external/internal/Bazel distinctions; exclude fake-clock cancellation/backpressure tests from generic override. | Medium: shorter periodic interval can split batches and alter counts; retain batching thresholds and assert exact per-key totals. | All delivery tests and backpressure/cancellation tests; ×50; compare RPC counts. |
| webdriver-result-poll | Replace exponential refresh waits with capped .1–.25s polling and explicit deadline. | `enterprise/server/test/webdriver/invocation/invocation_test.go:290` | 0–3.1s if ready during a 3.2s window; failure sleep ceiling is 25.5s. | Keep action page cacheability, served-from-cache and MilliCPU regex checks and action result readiness. | Medium: refresh load; preserve lazy ExecuteResponse caching coverage. | Full invocation webdriver target on browser-enabled VM; record refresh count and elapsed time. |
| build-logs-poll-cap | Cap microsecond-doubling log/readiness helpers at 10–25ms; retain final exact diff. | `enterprise/server/test/integration/build_logs/build_logs_test.go:147,164` | Up to .499s per 524.288ms detection sleep with 25ms cap; two static caller sites, dynamic looped use. | Keep completed/in-progress log scenarios, live/stable chunks, full exact logs, and EOF behavior. | Low/medium: unnecessary very early microsecond RPC attempts; overall old failure budget ≈1.048575s must not shrink accidentally. | Both build_logs tests, multiple progress updates, repeated under load. |
| executor-download-fixture | Reduce 128MiB input to just above BatchReadLimitBytes=2MiB (e.g. 3MiB), prove ByteStream Read still selected; replace 5-minute fake run with cancel-aware gate. | `enterprise/server/remote_execution/executor/executor_test.go:570,599` | No validated seconds estimate yet; 125MiB less allocation/upload per timeout test. Five-minute goroutine sleep may not delay target exit: do not claim 600s. | Retain ByteStream timeout, Unavailable classification, no internal retry, runner recycle, COMPLETED and input timestamps; preserve Send and Ping failure cases. | Medium: upload/chunking code may introduce other thresholds; instrument method selection before claiming same path. | InternalInputDownloadTimeout / PublishFailures, prove ByteStream RPC used; full target, leak/race check. |
| tasksize-ready | Replace unbounded 1s model readiness loop with bounded 10–25ms condition wait or model-loaded signal. | `enterprise/server/tasksize_model/tasksize_model_test.go:49` | Up to .975s per final detection window; repeated polls depend on model load, not measured. | Real model initialization and every prediction example remain; add a timeout without suppressing failures. | Low/medium: keep real model and wait for successful Predict; don’t fake the model whose behavior is asserted. | TestSamplePrediction repeatedly; record model-ready time and every expected prediction. |
| rbe-command-barriers | Replace completion-driving sleep 5 in merged cancellation with explicit action gate; synchronize restart action lifetime; keep short lease tests intact. | `enterprise/server/test/integration/remote_execution/remote_execution_test.go:1768,1993` | Up to 5s per completion-driven sleep minus event coordination; cancellation-merging test certainly waits for completion. Other sleep 10 commands are not automatically 10s costs. | Keep same op IDs, cross-org nonmerge, cancellation isolation, successful merged responses, Redis restart path; unblock only after assertions/events. | High: gates must be reachable by real executor and cleaned up; don't accidentally test only fake execution or change action digest identity. | CancellationDoesntAffectMergedActions and RedisRestart, then all merging/lease/failover tests; no generic t.Parallel (metrics/flags shared). |
| testserver-readiness **SHARED** | Reduce 500ms readiness polling to 25–50ms, retain 60s timeout and health endpoint; instrument startup count. | `server/testutil/testserver/testserver.go:21,162` | 0–.475s/start; average .2375s/start only under uniform readiness phase assumption. S starts → conditional .2375×S total; runtime S not measured. | Same process and exact OK readiness check, shutdown and race checks retained. | Low/medium: aggregate health RPC load; don't lower startup safety timeout. | All binary-backed integration targets; before/after startup metrics; serialize with harness consumers. |
| rbe-registration-poll **SHARED** | Reduce executor-set readiness polling from 100ms to 5–10ms; keep exact ID set convergence and 60s deadline. | `enterprise/server/test/integration/remote_execution/rbetest/rbetest.go:1064` | Up to .09–.095s per last detection window; E setup calls means conditional .09×E total, runtime E unmeasured. Used across RBE server/executor setup. | Real scheduler registration and exact expected executor IDs still required; preserve add/remove/failover behavior and all original deadlines. | Low/medium: more registration RPCs; registrations may propagate across app nodes asynchronously. | All rbetest consumers, especially multi-app/executor convergence and unregister; serialize harness changes and timing runs. |
| packet-capture-drain **SHARED** | Replace tcpdump Stop's 1s wait with supported immediate delivery/capture barrier and expected-packet drain. | `server/testutil/testnetworking/testnetworking.go:109` | Up to 1s per Stop minus actual drain; runtime occurrences not measured. | Keep real packets/capture and verify expected packet records arrive before SIGINT; no drop of late packets. | High: tcpdump output flush != all packets captured; prove barrier on supported capture mode. | Networking consumers with packet-count assertions, repeated under load; serialize harness changes. |

## Rejected / deferred

- **OOM killer long sleeps:** `oomkiller_test.go:454,462,470,500` advances fake time in `synctest.Test`; 10+1+10+1 seconds is **0 seconds intentional real waiting**. Leave unchanged. Source command: `sed -n '440,505p' enterprise/server/remote_execution/executor/oomkiller/oomkiller_test.go`.
- **Large timeout metadata values:** executor Timeout table has 10-minute/1-hour values, and workflow TestTimeout has 1-hour/2-hour values, but asserts serialized timeout settings after fake execution. They do not wait those durations. Source: `sed -n '737,785p' enterprise/server/remote_execution/executor/executor_test.go` and `sed -n '2519,2565p' enterprise/server/workflow/service/service_test.go`.
- **5-minute publish-failure sleep:** fake runner starts asynchronously; a 300s declaration is not proof of a 300s test. Included only as cancel-aware cleanup improvement in its owner row, with no claimed sleep savings.
- **Guard deadlines:** CLI, gateway, Raft GC and most context deadlines are failure bounds. `rpcutil_test.go:567` streaming path explicitly cancels after first response, so its 5s context is not a 5s cost. `fetch_server_test.go:457` 2s follower deadline encloses a **20ms fetch budget** (`:212`); shorten the governing fetch budget only with leader/follower synchronization, not the unrelated outer timeout.
- **Raft keepalive:** nodeliveness `:112` and rangelease `:181` already use 100ms leases / 50ms grace and sleep **200ms once each**. Further real-time reduction risks scheduler-dependent coverage. Fake-clock injection is plausible but low priority for only .4s total; maintain renewal beyond original expiry, not merely Valid immediately after Lease.
- **Usernet UDP lifetime:** `usernet_test.go:297` sleeps 500ms/4 **12 times = 1.5s**, deliberately spanning three idle timeouts. Do not remove the elapsed-time scenario. A fake clock requires network-loop injection; namespaces/root prerequisites complicate it.
- **Rate limiter:** migration-cache `TestCopyDataInBackground_RateLimitMax` explicitly asserts elapsed time ≥1s (`:752`). Speeding its rate setting alone weakens its asserted scenario. Other migration-cache backoff helpers are capped-poll candidates, but no runtime frequency/timing was established; defer instead of fabricating savings.
- **CPU/memory statistics:** commandutil stats tests use 500MB/250MB allocations and 2–3s workloads, assert absolute CPU and peak-memory ranges, and stagger children. A smaller fixture with unchanged absolute assertions fails; weaker assertions are prohibited. Only the separate 1s process-group timeout (`commandutil_unix_test.go:209`) is a possible follow-up with child readiness, worth <1s.
- **Chunking fixtures:** 100MiB data in `server/remote_cache/chunking/chunking_test.go:273,336` and `enterprise/server/remote_cache/chunking/chunking_test.go:53` exercises multi-chunk assembly/batching. Preserve chunking, batch boundaries, compression/incompressibility and exact hashes; no generic shrink proposal. A 1GB cache capacity constant is not a 1GB fixture allocation.
- **TS:** `app/util/async_test.ts:4,8` uses default **1ms** timers (11 calls in cancellation/delayed result cases, approximately .011s intentional timer floor). `app/util/get_tree_test.ts` already uses Jasmine fake timers for reconnects. No multi-second TS sleep found by the linked scan (the additional TSX React test has no timers); fake timers for async tests are low return and can change promise/microtask semantics. Don't parallelize tests that temporarily mutate global requestAnimationFrame or install Jasmine clocks.
- **Blanket parallelization:** workflow integration has 9 tests, hosted Bazel 11, RBE integration 55, Pebble 57, executor 9, all with 0 `t.Parallel()` sites. Absence alone proves nothing: `flags.Set`, shared metrics, package state, runfiles, Redis and runner pools need auditing. Pure error-code cases above are narrower candidates. Sharing DB/Redis across cases is not safe without isolation proof; one Podman duplicate was proven by full-body equality; only that duplicate is proposed for removal.
- **Docker/Firecracker/privileged tests:** installed Docker and `/dev/kvm` are not proof of usable dependencies. Docker server reports 29.1.3; current user cannot read /dev/kvm, and command lookup found no Podman or Firecracker. See [environment probes](survey-evidence/environment.txt). The actual singleflight failure is recorded in Measurements; tests needing a daemon, supported Podman/runtime or privileged networking require their appropriate CI/baremetal lane. No skips, manual tags, assertion loosening or CI configuration changes are proposed.


### Other variable-duration sleeps checked

[Variable definitions and callers with line numbers](survey-evidence/variable-sleep-context.txt) were extracted by a Python source-context scan (matching duration/readDelay/pauseDelay/updateInterval/benchmark flags/randSleepMillis and adjacent lines):

- `tools/tasksize_stress/tasksize_stress_test.go:11,61`: duration defaults **15s**; three memory workloads each hold 1/2/4GB for this duration, four CPU workloads run 1/2/4/8 busy goroutines for it. Existing BUILD is `manual`, `no-cache`, six shards. This is an explicitly selected stress workload, not accidental normal-suite waiting. Leave it unchanged; no new manual tag is proposed.
- CAS proxy atime helper `:221–239` uses 1ms doubling across up to ten waits: **1.023s failure ceiling**, stopping when an actual RPC count arrives. Its negative helper `:243–249` runs **ten 5ms waits = .05s per call** while advancing fake time. A cycle acknowledgement can help; simply asserting count=0 immediately would weaken coverage. Deferred, not counted in headline gains.
- OCI runtime pause test `ociruntime_test.go:917,961,976`: counter update **10ms**, condition polling 10ms until increment, one **100ms** negative observation while paused, then condition wait after unpause. The negative window spans ten real updates; retain it or prove equivalent suspended-process observation.
- Chunking test fake readers (`server/remote_cache/chunking/chunking_test.go:688,757`) delay **5ms per Read**, deliberately modeling cancellation/slow readers; runtime count depends on chunk/read size. Do not remove the slow-read scenario.
- Container pause fake uses **10ms** at its three construction sites `:717,741,758`; preserve concurrent pause serialization. The 1s pull case is separately ranked.
- ByteStream proxy **benchmark-only** RTT defaults **30ms ±6ms** (`:62,3240`), upload delay **16ms per simulated upload unit** (`:64,3224`). These are workload parameters; don't credit them to ordinary `go test` duration.
- `randSleepMillis` in RBE integration `:2769` actually uses `time.Duration(r)` without multiplying by `time.Millisecond`. Calls pass 0–100 / 0–50, so current waits are **0–99ns / 0–49ns**, not 100ms/50ms. No speedup credited; changing this would be a coverage/timing correctness task, not a test speedup.


