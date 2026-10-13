# Task: fill test gaps on an existing PR branch (t-ext4-mkfs, PR #6)

## Setup (do this first)
This work goes on an existing branch, not master:
```
git fetch origin t-ext4-mkfs && git reset --hard origin/t-ext4-mkfs
```
Read `result.md` in the repo root and `git log origin/master..HEAD` to understand the change. A reviewer approved it but listed missing tests.

## Goal
Add the tests below. Don't change behavior unless a new test exposes a real bug. If one does, fix it minimally and explain it in result.md.

- Prove the tmpfs path is actually taken. When privileged, assert that the unpack dir is on tmpfs (statfs magic), and exercise the size-exhaustion retry/fallback path. Right now every test case can pass through the disk fallback.
- Cancellation after a partial extraction, not only before extraction starts. Check that the process stops, temp dirs and mounts are cleaned up, and no partial image is left.
- Direct `fitsTmpfs` coverage: manifest errors, totals across multiple layers, and the exact-limit boundary.

## Acceptance
- Run the new tests **uncached** (`bazel test --nocache_test_results ...`) for every touched package, and they pass. If a test needs root, e.g. for a tmpfs mount, make it skip cleanly when not root, and run it once as root on this VM to show that it passes.
- Commit on the current HEAD (don't squash or rewrite the existing commits).
- Rewrite `result.md` as the full PR description, using the existing result.md content plus a short "Tests added" section.
