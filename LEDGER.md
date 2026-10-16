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
| I1 | `batch_operator` sender (atime_updater on cache proxy; also routing_* clients, migration_operators) — shutdown | sender's in-flight callback uses context.Background(); shutdown only closes quit → sender + remote RPC outlive shutdown | Codex lk-w1 (investigate); Claude V1 (verify) | testleak + active-RPC counter + /proc/self/fd | Investigator 3/3 on baseline (N=50 shut-down updaters → 50 stuck RPCs/sender stacks) | **Medium** (atime updater enabled in prod cache proxy; impact at shutdown/hung remote; one updater per process) | 119 gor / 50 active RPCs @N=50 | 31 gor / 0 RPCs | `leak/fix-bsproxy` 821b91df | `leak/repro-bsproxy` 05392d7a | IN PROGRESS (V1) | not started |
| I1-neg | Cache proxy ByteStream Read/Write (incl. chunked), CAS BatchRead/GetTree: success/cancel/remote err/deadline/local err, N=1/10/50 | per-request goroutine/FD leak | Codex lk-w1 | testleak + FD multiset + remote active counter | **No leak** (3 runs × 27 groups, 4131 reqs) | High (hot path) | stable | — | — | audit tests on `leak/repro-bsproxy-audit` 3fd12168 (candidate for enforcement) | — | — |
| I2 | OCI fetcher: `mirrorTransport.RoundTrip` (ocifetcher.go) mirror non-2xx fallback | Discarded mirror response body never closed → socket FD + 2 http goroutines held until request ctx cancel | Codex lk-w2 (investigate) | testleak + local /proc/self/fd helper, keepalive off | Reproduced by investigator 3/3 (N=1/10/50: +1 FD, +2 goroutines per op, exact linear) | **Low** (`executor.container_registry_mirrors` not in static prod config; flagd unknown). Bounded by ctx cancellation (control test 3/3) | +50 FD/+100 gor @N=50 | 0/0 @ all N | `leak/fix-oci` a933fec9 (1-line Body.Close) | `TestMirrorFallbackClosesResponseBodies` on `leak/repro-oci` | PENDING | not started |

## Detector improvements
| ID | Change | Branch | Validation (leak caught / clean accepted / bounded not flagged) | Status |
|---|---|---|---|---|

## False positives / non-leaks
| ID | Observation | Why not a leak |
|---|---|---|
| I2-fp1 | ocifetcher_server_proxy upstream stream w/o explicit cancel on early return | singleflight cancels shared ctx when final waiter exits |
| I2-fp2 | testregistry.Run servers left running in tests | test fixture lifetime, not fetch cleanup |
| I1-fp1 | 1 extra goroutine in count polling | testify `Eventually` spawns predicate goroutine — harness artifact; use synchronous polling |

## Enforcement progress
| Test target | Check enabled | Stable runs | Notes |
|---|---|---|---|
