# Task (survey, no PR): Go module dependencies, what can be removed or replaced
## Your area
Every module in `go.mod` (and how it shows up in Bazel: `deps.bzl`/`MODULE.bazel` go_repository entries, gazelle). For each direct dependency:
- Which first-party packages import it, and how many? (`go list`, grep, or `bazel query 'rdeps(//..., @com_github_x//...)'`)
- Is it used by only 1–2 call sites that could be swapped for the stdlib or an existing dep?
- Duplicates: two libraries for the same job (several YAML/JSON/logging/retry/uuid/errors/cloud SDK packages, multiple versions).
- Heavy transitive trees: which direct deps pull in the most indirect modules? (`go mod graph`, `go mod why -m`)
- Deps that are only test-only, tool-only, or dead.
Also count the totals (direct, indirect, go_repository rules) so later work can be measured against them.

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
