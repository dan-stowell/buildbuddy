# Task: Migrate the structured yaml.v2 callers to yaml.v3

## What to do
Move these yaml.v2 importers to the already-present gopkg.in/yaml.v3: cli/config, cli/plugin, enterprise/server/cmd/ci_runner, enterprise/server/hostedrunner, enterprise/server/workflow/config, enterprise/server/workflow/service, enterprise/server/test/integration/ci_runner. **Keep the workflow config (buildbuddy.yaml) parse behavior identical**: watch the v2→v3 differences (map[interface{}]interface{} vs map[string]interface{}, unknown-field strictness, int/float decoding, empty docs). Don't touch cli/translate/yaml (it's being deleted in another task).

## Scope (files you may edit)
the listed packages' source, tests, and BUILD files

## Expected gain and how to verify
Removes 7 of 8 first-party yaml.v2 import edges. Add golden tests for buildbuddy.yaml parsing if coverage is thin.

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
