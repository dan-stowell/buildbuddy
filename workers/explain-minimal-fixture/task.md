# Task: cli explain test: no Bazel/Git setup for fixture-log cases

## What to do
cli/test/integration/explain/explain_test.go's two cases only read recorded execution logs, but they set up a full Bazel workspace and Git init. Use a temp working directory with just the fixture logs, keeping the exact diff/output checks. Verify the explain command doesn't need a workspace for these inputs.

## Scope (files you may edit)
cli/test/integration/explain/explain_test.go (BUILD if deps change)

## Expected saving
Workspace + Bazel setup × 2 cases.

## Context
BuildBuddy monorepo (Bazel). The owner wants **faster tests, especially the slow integration tests, while preserving coverage**. A survey of the integration-test harnesses found this task. Its savings are mostly projections, not measurements: **measure the before/after yourself.** Its numbers are models: **re-verify the premise first**, and if it's wrong or the saving isn't real, stop and explain why in result.md.

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

## Sharing setup without losing coverage
If you share a fixture (app, server, DB process) across cases, each case must keep its own isolation: separate API keys/groups/invocation IDs/DB names, as appropriate. No case may depend on another case's leftovers, and a failure in one case must not cascade. Say explicitly in result.md how isolation is preserved. If sharing would change what a case actually tests (e.g. a case is *about* startup or migration), don't share for that case.
