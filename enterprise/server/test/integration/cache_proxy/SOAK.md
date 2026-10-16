Run explicitly (manual target; does not run with wildcard suites):

```
bb remote test --config=remote //enterprise/server/test/integration/cache_proxy:soak_test --test_output=all --test_arg=-test.v --runs_per_test=3
```

Validate detector sensitivity (expected failure, +1 goroutine and /dev/null FD per batch):

```
bb remote test --config=remote //enterprise/server/test/integration/cache_proxy:soak_test --test_output=all --test_arg=-test.v --test_env=SOAK_INJECT_LEAK=1
```

Two warmups and ten measured batches use a live app and Pebble-backed proxy.
Each batch uses a fresh instance to avoid unbounded client creation or accidental
cache-only traffic. Workers remain owned by the live services during sampling;
growth candidates require investigation, and a plateau is not a leak.
No distributed cache/lookaside is included. Pool size is 2, hit tracker workers
3 at 100ms, atime flush 100ms (scaled from production 1s), FMB TTL 1s.
