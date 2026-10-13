# Task: Shared test server readiness: poll faster

## What to do
server/testutil/testserver/testserver.go (~lines 21, 162) polls server readiness every 500 ms (with a 60s timeout). **Every integration test that starts a BuildBuddy server goes through this.** Reduce it to 25–50 ms, keeping the 60s timeout and the health endpoint. Measure: count server starts across the main integration targets, and compare the wall time of 3–4 representative integration targets before and after (remotely). This is shared infrastructure, so be conservative and keep the change minimal.

## Scope (files you may edit)
server/testutil/testserver/testserver.go only

## Expected saving
~0.24s per server start on average; multiplies across all integration tests.

## Context
BuildBuddy monorepo (Bazel). The owner wants **faster tests, especially the slow integration tests, while preserving coverage**. A survey of time-wasting patterns in test code found this task (some numbers were measured locally, others are estimates). Its numbers are models: **re-verify the premise first**, and if it's wrong or the saving isn't real, stop and explain why in result.md.

## Preserving coverage (hard requirement)
Don't delete, skip, `manual`-tag or weaken any test case, assertion, or scenario. Allowed: sharding, sharing setup, exposing nested cases as top-level tests, replacing fixed sleeps or long waits with explicit readiness/condition signals, shorter intervals or timeouts *in tests* that still exercise the same path, and parallelizing independent cases. In result.md, **list the test cases before and after** (from `go test -list`, or the test XML/`bazel test` output) and show they're the same set (or a superset), with every case passing.

**Flakiness is the main risk.** A faster test that flakes is a regression. Run changed tests multiple times (`--runs_per_test=5` or more, or `-count`) and report the pass counts.

## How to build and test
This VM has only 2 CPUs; Bazel analysis alone can take 15+ min locally. **First try BuildBuddy Remote Bazel.** One-time setup (the remote runner can't reach the internal git mirror, so point the fetch URL at the public fork and keep pushes on the mirror): `git remote set-url origin https://github.com/dan-stowell/buildbuddy.git && git remote set-url --push origin https://github.int.exe.xyz/dan-stowell/buildbuddy.git`. Then: `bash -lc 'cd ~/repo && bb remote test <targets> <flags>'`. A login shell is needed so `BUILDBUDDY_API_KEY` is set; it runs the whole Bazel invocation on a remote runner, including your uncommitted/committed local changes. If `bb remote` hasn't started running within ~10 minutes (it stays stuck on "Waiting for available remote runner") or errors on auth/repo access, fall back to local `bazel test` in the background (`nohup ... &`, then poll), and say which you used. Tests needing Docker/podman/KVM/firecracker may only work remotely. If you can't run something at all, say so explicitly; don't claim it passes.

## Rules
- Edit only the files in scope, unless that's unavoidable (and if so, say why). Don't edit go.mod/MODULE/lockfiles or CI configs.
- Commit on the current branch. Don't push or open a PR.
- **Don't commit result.md** (it's git-ignored; leave it untracked). When you're finished, run `git remote set-url origin https://github.int.exe.xyz/dan-stowell/buildbuddy.git` so the PR tooling works.

## Deliverable: `result.md`
What you changed; before/after timings (the same method both times, saying whether they're remote or local); the before/after test-case lists; repeated-run pass counts; and anything you chose not to do.
