# BuildBuddy Goroutine / FD Leak Ledger

Baseline: dan-stowell/buildbuddy `master` @ 770b88bc8 (upstream mirror). Coordinator: leak-coord.
Branch convention: `leak/det-*` detector work, `leak/repro-*` reproducers (no fix), `leak/fix-*` fixes (on top of repro), `leak/ledger` this file.

## Existing detector inventory (2026-10-16)
- `server/testutil/testleak.Check(t, opts...)`: goleak.Find at t.Cleanup with IgnoreCurrent. Goroutines only; no FD tracking; no accumulation/repeat helper. goleak.Find retries internally (~default backoff up to ~2s? to be verified by D1).
- Users: grpc_client_test, healthcheck_test, db_test, pubsub_test, scheduler_server_test.
- Direct goleak users: disk_cache_test (VerifyTestMain), rpcutil_test (VerifyNone).
- No FD leak detection anywhere in repo.

## Investigations
| ID | Subsystem / lifecycle | Hypothesis | Agent(s) | Detector | Repro | Prod rel. | Baseline meas. | Fixed meas. | Candidate fix | Regression test | Indep. verify | Human review |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| I3a | App `pooledByteStreamClient` pool cache (`grpc_client.enable_pool_cache=true`) | connPoolMap caches a 2-conn pool per target (scheme+userinfo+host:port from request URL) forever; no eviction | Claude I3 (investigate); Codex lk-w3 (V4 verify) | stack-substring counts + /proc/self/fd | Investigator 10/10 (N=50 targets: +800 gor unreachable; +1300 gor/+200 FD live in-proc) | **Medium** (user-influenced input; restrict_bytestream_dialing=true in prod limits host but not userinfo) — possibly security-sensitive (resource exhaustion) | see report | bounded by idle sweep | `leak/fix-i3-bytestream-pool-cache` a48761d4 (idle sweep + new flag) | `leak/repro-i3-bytestream-pool-cache` 054a6c2f | IN PROGRESS (V4) | not started |
| I3b | `hit_tracker_client` shutdown | senders use never-cancelled server ctx; shutdown wg.Wait() w/o deadline → hung Track RPC blocks graceful shutdown forever | Claude I3; Codex lk-w3 (V4) | shutdown latency + goroutine stacks | Investigator 10/10 | **Medium** (cache proxy; stuck app) | blocked >10s w/ 2s max_shutdown | completes at deadline | `leak/fix-i3-hit-tracker-shutdown` 0f8be418 | `leak/repro-i3-hit-tracker-shutdown` 43345ec4 | IN PROGRESS (V4) | not started |
| I3-neg | grpc_client ClientConnPool dial/close ×61, 5000 RPCs, 20 server restarts, unreachable target; hit_tracker & atime normal/failing shutdown up to 20k items | — | Claude I3 | counts + FD | **No leak** | High | flat | — | — | measurement tests `leak/repro-i3-measurements` 99c88a1a | — | — |
| I4a | commandutil `RunWithProcessTreeCleanup` cancel after root reaped (bare runner; in-guest vmexec for Firecracker incl. workflows) | Background child inherits stdout/stderr; cancel walks descendants from reaped root → child not killed → Wait blocks; pipes+goroutines+child retained | Codex lk-w2 (investigate); Claude V3 (verify) | testleak + /proc/self/fd pipe targets + child liveness + return latency | Investigator 3/3 (N=50: +100 pipe FDs, +150 gor, 50 live children, 0/50 returned in 1s) | **Medium** (bare pool + vmexec guests; requires background child holding stdio) | see report | 0 growth, all return in <0.2s | `leak/fix-cmd` 26ae7c77 (SIGKILL original pgid) | `leak/repro-cmd` 0f548864 | IN PROGRESS (V3) | not started |
| I4b | commandutil `forwardSignals` | forwarder goroutine survives command completion until caller ctx done | Codex lk-w2; Claude V3 | testleak stack count | Investigator 3/3 (+1 gor/op w/ open ctx) | **Low** (prod action ctx cancelled after exec bounds it) | +50 @N=50 | 0 | `leak/fix-cmd` | `leak/repro-cmd` | IN PROGRESS (V3) | not started |
| I4-neg | commandutil success/exit≠0/deadline/cancel/SIGKILL/output-limit N=1/10/50 | — | Codex lk-w2 | testleak+FD | No leak | High | stable | — | — | `TestRun_RepeatedLifecycleResources` (candidate) | — | — |
| I1 | `batch_operator` sender (atime_updater on cache proxy; also routing_* clients, migration_operators) — shutdown | sender's in-flight callback uses context.Background(); shutdown only closes quit → sender + remote RPC outlive shutdown | Codex lk-w1 (investigate); Claude V1 (verify) | testleak + active-RPC counter + /proc/self/fd | Investigator 3/3 on baseline (N=50 shut-down updaters → 50 stuck RPCs/sender stacks) | **Medium** (atime updater enabled in prod cache proxy; impact at shutdown/hung remote; one updater per process) | 119 gor / 50 active RPCs @N=50 | 31 gor / 0 RPCs | `leak/fix-bsproxy` 821b91df | `leak/repro-bsproxy` 05392d7a | IN PROGRESS (V1) | not started |
| I1-neg | Cache proxy ByteStream Read/Write (incl. chunked), CAS BatchRead/GetTree: success/cancel/remote err/deadline/local err, N=1/10/50 | per-request goroutine/FD leak | Codex lk-w1 | testleak + FD multiset + remote active counter | **No leak** (3 runs × 27 groups, 4131 reqs) | High (hot path) | stable | — | — | audit tests on `leak/repro-bsproxy-audit` 3fd12168 (candidate for enforcement) | — | — |
| I2 | OCI fetcher: `mirrorTransport.RoundTrip` (ocifetcher.go) mirror non-2xx fallback | Discarded mirror response body never closed → socket FD + 2 http goroutines held until request ctx cancel | Codex lk-w2 (investigate) | testleak + local /proc/self/fd helper, keepalive off | Reproduced by investigator 3/3 (N=1/10/50: +1 FD, +2 goroutines per op, exact linear) | **Low** (`executor.container_registry_mirrors` not in static prod config; flagd unknown). Bounded by ctx cancellation (control test 3/3) | +50 FD/+100 gor @N=50 | 0/0 @ all N | `leak/fix-oci` a933fec9 (1-line Body.Close) | `TestMirrorFallbackClosesResponseBodies` on `leak/repro-oci` | PENDING | not started |

## Detector improvements
| ID | Change | Branch | Validation (leak caught / clean accepted / bounded not flagged) | Status |
|---|---|---|---|---|

## False positives / non-leaks
| ID | Observation | Why not a leak |
|---|---|---|
| I3-fp1 | hit_tracker Register pool never closed | intentional: post-shutdown hits still sent through it; process-lifetime |
| I3-fp2 | remote auth key refresher, flagd, quota subscribers never stopped | process-lifetime singletons |
| I4-fp1 | OOM killer loop, OCI exec WaitDelay paths | service-owned / already bounded |
| I2-fp1 | ocifetcher_server_proxy upstream stream w/o explicit cancel on early return | singleflight cancels shared ctx when final waiter exits |
| I2-fp2 | testregistry.Run servers left running in tests | test fixture lifetime, not fetch cleanup |
| I1-fp1 | 1 extra goroutine in count polling | testify `Eventually` spawns predicate goroutine — harness artifact; use synchronous polling |

## Enforcement progress
| Test target | Check enabled | Stable runs | Notes |
|---|---|---|---|
