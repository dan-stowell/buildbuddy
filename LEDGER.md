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
