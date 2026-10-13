# Task: Finish the Docker→OCI migration in six tool packages

## What to do
Each of these packages defines both legacy rules_docker image/push rules and OCI (rules_oci) equivalents: tools/tcpproxy, tools/cacheload, tools/smarter_device_manager, enterprise/tools/mdload, codesearch/cmd/server, tools/probers. For each one, delete the legacy Docker image/push rules and keep the OCI ones. If any label might be invoked by scripts, CI, docs or deploy configs (grep the repo, including .github, buildkite, deployment, docs), add an `alias` from the old label to the OCI target, or update the caller. Check that each OCI image has the same payload (binary, files, entrypoint, base image) as the legacy one; if one isn't equivalent, fix the OCI rule or skip that package and explain. **One commit per package.** Don't touch enterprise/tools/rbeperf (another task owns its BUILD).

## Scope (files you may edit)
the six packages' BUILD files; callers you find, if they reference removed labels

## Expected gain and how to verify
−~12 rules_docker targets; brings rules_docker closer to removal. Verify: bazel build each OCI image target, and compare the image config and layers against the legacy image where feasible.

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
