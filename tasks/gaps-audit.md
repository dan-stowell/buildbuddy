# Task: fill test gaps on an existing PR branch (t-ext4-audit, PR #7)

## Setup (do this first)
This work goes on an existing branch, not master:
```
git fetch origin t-ext4-audit && git reset --hard origin/t-ext4-audit
```
Read `result.md` in the repo root and `git log origin/master..HEAD` to understand the change. A reviewer approved it but listed missing tests.

## Goal
Add the tests below. Don't change behavior unless a new test exposes a real bug. If one does, fix it minimally and explain it in result.md.

- Concurrent callers for the same image, one with invalid and one with valid credentials. The auth failure must not make the valid caller fail. Test both orderings: invalid first, and valid first.
- Cancellation of a caller while it waits for the conversion lock. It returns promptly with a context error, and the holder's conversion still completes for the other waiters.
- Serialization that ignores credentials, in legacy-cache mode (`executor.local_cache_store_ext4_images=false`).

## Acceptance
- Run the new tests **uncached** (`bazel test --nocache_test_results ...`) for every touched package, and they pass. If a test needs root, e.g. for a tmpfs mount, make it skip cleanly when not root, and run it once as root on this VM to show that it passes.
- Commit on the current HEAD (don't squash or rewrite the existing commits).
- Rewrite `result.md` as the full PR description, using the existing result.md content plus a short "Tests added" section.
