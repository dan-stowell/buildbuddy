# Raft client ErrorCodes fake-time results

Changed only enterprise/server/raft/client/client_test.go: imported testing/synctest and wrapped each ErrorCodes subtest body in synctest.Test. All 12 table entries, assertions, 3-second context deadlines, 1-second operation timeout, and production default retry logic are unchanged. Go 1.27.2 supports synctest.Test; the OOM killer tests already use it. No BUILD dependency is required for this standard-library import.

## Measurements

Both measurements used BuildBuddy Remote Bazel with the same command:

```sh
bb remote test //enterprise/server/raft/client:client_test --test_filter=TestRunNodehostFn_ErrorCodes --test_arg=-test.v --test_output=all --nocache_test_results
```

Go verbose test-body duration: **18.00s before, 0.00s after** (rounded to hundredths), saving approximately 18 seconds per run. Before, six temporary-error cases each took 3.00s. After, all cases reported 0.00s. Bazel invocation elapsed times were 37.683s and 11.859s respectively; these include build/analysis/cache effects and are not the test-body saving.

Before invocation: https://app.buildbuddy.io/invocation/f8e6a993-8996-46ef-97db-7fc3c495d381

After invocation: https://app.buildbuddy.io/invocation/b90523b5-9ad1-4910-9aa9-8be337935db5

## Case lists from Bazel verbose test output

Before (all passed):

```text
TestRunNodehostFn_ErrorCodes
TestRunNodehostFn_ErrorCodes/ErrShardNotReady
TestRunNodehostFn_ErrorCodes/ErrShardClosed
TestRunNodehostFn_ErrorCodes/OutOfRange
TestRunNodehostFn_ErrorCodes/ErrShardNotFound
TestRunNodehostFn_ErrorCodes/ErrRejected
TestRunNodehostFn_ErrorCodes/ErrTimeout
TestRunNodehostFn_ErrorCodes/ErrSystemBusy
TestRunNodehostFn_ErrorCodes/ErrAborted
TestRunNodehostFn_ErrorCodes/ErrCanceled
TestRunNodehostFn_ErrorCodes/ErrClosed
TestRunNodehostFn_ErrorCodes/ErrShardAlreadyExist
TestRunNodehostFn_ErrorCodes/ErrShardNotStopped
```

After (all passed):

```text
TestRunNodehostFn_ErrorCodes
TestRunNodehostFn_ErrorCodes/ErrShardNotReady
TestRunNodehostFn_ErrorCodes/ErrShardClosed
TestRunNodehostFn_ErrorCodes/OutOfRange
TestRunNodehostFn_ErrorCodes/ErrShardNotFound
TestRunNodehostFn_ErrorCodes/ErrRejected
TestRunNodehostFn_ErrorCodes/ErrTimeout
TestRunNodehostFn_ErrorCodes/ErrSystemBusy
TestRunNodehostFn_ErrorCodes/ErrAborted
TestRunNodehostFn_ErrorCodes/ErrCanceled
TestRunNodehostFn_ErrorCodes/ErrClosed
TestRunNodehostFn_ErrorCodes/ErrShardAlreadyExist
TestRunNodehostFn_ErrorCodes/ErrShardNotStopped
```

The ordered lists are identical: one parent test and all 12 subtests.

## Repeated verification

```sh
bb remote test //enterprise/server/raft/client:client_test --test_arg=-test.v --test_output=all --nocache_test_results --runs_per_test=5
```

The entire client target passed 5/5 runs. Every ErrorCodes subtest passed 5/5 (60/60 case executions), and TestSession, TestSessionInParallel, and TestRefreshSession each passed 5/5. ErrorCodes parent durations were 0.00s in four runs and 0.01s in one. No failures or bubble compatibility issues occurred.

Repeated invocation: https://app.buildbuddy.io/invocation/2adaeefb-34dc-4ec9-9ed9-25b202a18cfa

Logs: /tmp/raft-before-verbose.log, /tmp/raft-after.log, /tmp/raft-repeat.log. git diff --check passed.

No parallel fallback, shortened deadlines, production changes, dependency changes, skipped cases, or new assertions were needed. Local Bazel was not needed. Five successful repetitions provide flakiness evidence, not a guarantee. result.md is left uncommitted as requested.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3