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

## Detector improvements
| ID | Change | Branch | Validation (leak caught / clean accepted / bounded not flagged) | Status |
|---|---|---|---|---|

## False positives / non-leaks
| ID | Observation | Why not a leak |
|---|---|---|

## Enforcement progress
| Test target | Check enabled | Stable runs | Notes |
|---|---|---|---|
