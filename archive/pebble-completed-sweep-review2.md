1. **Task completion: partial.** Both 3-second sleeps are removed, and existing cases and assertions remain. The cutoff test waits for a completed janitor sweep. The minimum-age test waits only for a sampling batch, so it does not establish the required completed eviction sweep with zero evictions. `result.md` reports timings and 10/10 passing runs; I did not independently rerun tests.

2. **Correctness / coverage bug — P2:** [pebble_cache.go:3322](/home/exedev/repo/enterprise/server/backends/pebble_cache/pebble_cache.go:3322) closes the observation channel after sampling, regardless of whether samples were rejected or enqueued. [pebble_cache_test.go:1685](/home/exedev/repo/enterprise/server/backends/pebble_cache/pebble_cache_test.go:1685) then immediately checks retention. If an age-filter regression enqueues young entries, the separate janitor/deletion workers can still be delayed, allowing retention assertions to pass before erroneous eviction occurs. Assert that the observed batch rejected young entries and enqueued none, or synchronize through eviction completion.

3. **Missing tests:** Add a deterministic regression that delays the janitor/deletion workers and verifies that incorrectly accepting young samples cannot satisfy the negative-eviction check. The added ordering tests verify fresh observation boundaries but do not cover this gap.

VERDICT: changes-needed
