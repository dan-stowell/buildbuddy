# Task (survey, no PR): dead targets, dead code, over-wide BUILD deps
## Your area
First-party code and targets that ship in no binary, image, or release.
- Find the roots: the binaries, OCI images, release and deploy targets, and anything the CI config builds (look at `.github/workflows`, buildkite configs, `tools/`, release scripts).
- Find packages and targets that no root reaches, outside their own tests (`bazel query 'deps(<roots>)'` versus `//...`; look for go_library targets whose only rdeps are their own tests).
- Find whole directories that look abandoned: experiments, old migrations, unused tools, prototype services. Check git log for their last meaningful change.
- Find over-wide deps: BUILD targets that depend on far more than they use (e.g. a small util that imports a whole server package), and big catch-all library targets that drag heavy deps into many binaries. Suggest splits.
- Find unused flags, feature gates, and config branches that are always on or always off, when removing them would let code go.
- Count the totals (targets in `//...` by rule kind, Go packages) as a baseline.

## Context
This is the BuildBuddy repo (Bazel monorepo: Go server and executor, TypeScript/React app, protos, enterprise/ tree). The owner wants to **shrink and simplify the build**, which means all of: fewer external dependencies, faster clean builds, fewer targets and less dead code, and smaller output binaries. Your survey feeds a backlog that ~20 parallel engineers will work from, each on one small, independent PR.

## Rules
- **Research only.** Do not open PRs or push. Prototype changes are fine for checking a claim (e.g. delete something and see if the build still passes), but don't commit them unless they're tiny and you think they'd help someone else.
- **Show numbers.** Back every claim with the command you ran and its output (trimmed). "Probably unused" doesn't count; "`bazel query 'rdeps(//..., X)'` returns only X's own test" does.
- If a full `bazel build //...` is too slow or fails in this VM, say so, then narrow the scope (e.g. `//server/... //enterprise/server/...`) and keep going.

## Deliverable: `result.md`
1. **Summary:** the 5 biggest wins in your area, with estimated impact.
2. **Measurements:** the raw data you collected, with the commands used.
3. **Candidate tasks:** a table where each row is one independent PR-sized task: `slug | what to do | files/dirs it touches | touches shared dep files? (go.mod, MODULE.bazel, deps.bzl, WORKSPACE, package.json, lockfiles) | expected gain (numbers) | risk | how to verify`. Aim for 10–30 rows, ranked by gain per effort. Keep the tasks disjoint: two rows shouldn't need to edit the same files, except for the shared dep files, which you should flag.
4. **Not worth it / blocked:** things you looked at and rejected, and why.
