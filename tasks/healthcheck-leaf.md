# Task: Move health-check types out of the interfaces catch-all

## What to do
server/util/healthcheck imports server/interfaces (53 direct deps) only for `Checker` and `CheckerFunc`. Move those types into a new leaf package (e.g. server/util/healthcheck/checker or server/interfaces/health, your choice), keep type aliases in interfaces (`type Checker = health.Checker`) so all existing code keeps compiling with identical types, and point healthcheck at the leaf.

## Scope (files you may edit)
server/interfaces/{interfaces.go,BUILD}, server/util/healthcheck/, the new leaf package

## Expected gain and how to verify
healthcheck standalone closure: −~152 first-party rules, −~70 external labels. Report the before/after of `bazel query 'deps(//server/util/healthcheck)' | wc -l`. Build the direct rdeps of healthcheck and interfaces (the latter can be large; build the server, enterprise server and executor binaries as a proxy).

## Context
BuildBuddy monorepo (Bazel). The owner is shrinking and simplifying the build: fewer external deps, less dead code, fewer targets, smaller binaries. A survey found this task. Its claims are estimates: **re-verify them before changing anything**, and if the premise turns out to be false (the code is used, the behavior can't be preserved, the gain is negligible), stop and explain why in result.md rather than forcing it through.

## Rules
- **Don't edit shared dependency files:** `go.mod`, `go.sum`, `MODULE.bazel`, `MODULE.bazel.lock`, `deps/*.MODULE.bazel`, `deps.bzl`, `WORKSPACE*`, `package.json`, or any lockfile. Another engineer removes now-unused external deps in a separate, serialized PR. If your change leaves an external module with no first-party users, say so in result.md (with the `bazel query`/grep evidence) and leave go.mod alone.
- Update BUILD files for the dirs you touch (by hand, or with gazelle limited to those dirs). Don't edit files outside the listed scope unless that's unavoidable, and if it is, say so.
- **Preserve behavior**, unless the task explicitly says it's deleting dead code. Add or adjust tests where the survey flags behavior risk.
- **Builds are slow here.** This VM has 2 CPUs and no remote execution. Run long bazel commands in the background (`nohup ... > /tmp/x.log 2>&1 &`) and poll; don't give up on a build because it's slow. A first build can take 30–60 min. Build and test only the targets your change affects plus their direct rdeps (`bazel query 'rdeps(//..., <changed targets>, 1)'`), not `//...`.
- Commit on the current branch with a clear message. Don't push or open a PR.

## Deliverable: `result.md`
What you changed, the before/after numbers (same command both times: targets, deps, external labels, LOC, as applicable), the build/test commands you ran and their results, any external modules now unused, and anything you chose not to do.
