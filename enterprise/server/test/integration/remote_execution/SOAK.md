Resource accumulation soak
==========================

Run explicitly (the target is tagged manual):

```
bb remote test //enterprise/server/test/integration/remote_execution:soak_test --test_arg=-test.v --test_output=all --experimental_ui_max_stdouterr_bytes=8388608 --runs_per_test=3
```

The test keeps one app and executor alive, warms them with two batches, then
measures ten batches of twenty actions. Each batch mixes successful execution,
nonzero exit, cache lookup, 3 MiB input/output transfer, action timeout, Execute
stream cancellation followed by WaitExecution, and a BES-interrupted invocation.
The server handler worker pool is disabled before servers start.

To validate detection, deliberately retain one goroutine and one FD after each
of the seventeen ordinary actions per batch:

```
bb remote test //enterprise/server/test/integration/remote_execution:soak_test --test_env=SOAK_INJECT_LEAK=1 --test_arg=-test.v --test_output=all --experimental_ui_max_stdouterr_bytes=8388608
```

This command **must fail** with `SOAK GROWTH candidate` and series increasing by
17 per measured batch. Synthetic resources are released after measurement.

`testleak.Soak` reuses the detector's goroutine parser and Linux FD reader and
socket resolver. It polls count vectors every 100ms until three successive
observations agree, with a five second deadline per sample. It prints every
normalized stack (function sequence plus creator function) and FD class, ranked
by final minus initial count. A monotone series is flagged if it increases in
at least two thirds of batches and its total increase is at least the number of
batches. A one-time pool expansion followed by a plateau is printed but not
flagged. No goroutine functions or FD classes are ignored.

A flag needs ownership investigation before being called a leak. This is a
process-local, Linux-only integration workload: Redis runs in a subprocess,
the OLAP handle is a fake, and the default test runner executes bare commands.
The cache backend is Redis; OLAP is a fake handle. It does not exercise OCI, VFS, Pebble/distributed cache, production connection
pool sizes, or resources inside Redis/action subprocesses. Existing service
workers remain alive during samples. The finite warmup and settling windows
cannot establish long-term boundedness. Oscillating groups, sparse growth, and
replacement of resources within a broad FD class can escape the heuristic.
