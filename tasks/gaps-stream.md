# Task: fill test gaps on an existing PR branch (t-ext4-stream, PR #8)

## Setup (do this first)
This work goes on an existing branch, not master:
```
git fetch origin t-ext4-stream && git reset --hard origin/t-ext4-stream
```
Read `result.md` in the repo root and `git log origin/master..HEAD` to understand the change. A reviewer approved it but listed missing tests.

## Goal
Add the tests below. Don't change behavior unless a new test exposes a real bug. If one does, fix it minimally and explain it in result.md.

- Assert that the OCI streaming integration test actually takes the streaming path when it should (for example, through a counter, log hook, or test seam). On non-root it should skip or assert the fallback explicitly, so it can't pass silently through the baseline.
- Automated baseline-vs-streaming comparisons for sparse files, long names (>100 chars and PAX), xattr handling, and fractional mtimes. Today these rely on the benchmark fixture.
- Hardlink equivalence for symlinks and special files: `enterprise/tools/oci_ext4_benchmark/compare.py:28` groups only regular files. Extend it, and add a test case.
- Firecracker guest boot validation stays out of scope.

## Acceptance
- Run the new tests **uncached** (`bazel test --nocache_test_results ...`) for every touched package, and they pass. If a test needs root, e.g. for a tmpfs mount, make it skip cleanly when not root, and run it once as root on this VM to show that it passes.
- Commit on the current HEAD (don't squash or rewrite the existing commits).
- Rewrite `result.md` as the full PR description, using the existing result.md content plus a short "Tests added" section.
