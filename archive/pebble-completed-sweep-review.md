1. **Task completion:** Mostly. Both 3-second sleeps are replaced with completion signals, and retention assertions remain. The minimum-age case observes a sampler batch; the threshold case observes the LRU janitor. [result.md](/home/exedev/repo/result.md) reports identical 214-case inventories and 5/5 repeated passes. The diff also extends into `approxlru.go`, beyond the original scope.

2. **Correctness bug — P2:** [pebble_cache_test.go:1591](/home/exedev/repo/enterprise/server/backends/pebble_cache/pebble_cache_test.go:1591) can time out despite correct eviction behavior. If the sampler is delayed until after the five fake-clock advances, or size accounting remains low during them, it can enter the fake-clock sleep at `pebble_cache.go:3246`. The new wait advances no fake time, so the sampler cannot finish a batch. Synchronize sampler readiness or keep advancing the clock until completion, while staying below the minimum eviction age.

3. **Missing tests:** Add a deterministic delayed-sampler/delayed-size-accounting case covering that schedule. The existing repeated runs do not force it. A focused hook test should also verify that an already-running sweep cannot satisfy a newly registered wait.

I did not rerun tests or modify files; validation counts above come from the supplied report.

VERDICT: changes-needed
