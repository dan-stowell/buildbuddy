1. **Task completion:** Partial, with a justified limitation. The higher-threshold test waits for accounting and a fresh completed sweep, preserving all retention assertions. The minimum-age test retains its sleep at [pebble_cache_test.go:1589](/home/exedev/repo/enterprise/server/backends/pebble_cache/pebble_cache_test.go:1589): its janitor blocks waiting for eligible samples, so a completed-sweep wait cannot finish while all entries remain young. This invalidates part of the task’s premise; [result.md](/home/exedev/repo/result.md) documents the limitation and approximately two-second saving.

2. **Correctness bugs:** None found. Channel registration and sweep capture are synchronized, and an already-running sweep cannot satisfy a newly registered wait. Changes to approxlru extend beyond the listed scope but place the hook where sweeps actually execute.

3. **Missing tests:** No blocking gaps. The ordering regression covers an in-progress sweep, and the modified integration test covers below-threshold completion. Direct cancellation and unknown-partition tests would be optional additions.

`result.md` reports both targets passing 5/5 runs with preserved case lists. I did not rerun tests; diff checks passed, and no files were modified.

VERDICT: approve