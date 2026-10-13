# Task (survey, no PR): non-Go external deps, Bazel rules, toolchains, JS/npm, protos
## Your area
Everything external that isn't a Go module: `MODULE.bazel`, `WORKSPACE`/`deps.bzl` http_archives, rule sets (rules_*), toolchains (C++, Java, Python, Node, Docker/OCI, protoc plugins), `package.json`/npm deps, and the proto toolchain.
- List every external repo or module, and check whether `//...` actually reaches it (`bazel query 'deps(//...)'` and `bazel mod graph`/`bazel mod explain`; then which first-party targets pull it in).
- Find rule sets or toolchains kept around for one or two callers (e.g. a Java or Python toolchain for a single script).
- For npm: unused packages, duplicate libraries for the same job, and heavy deps used in one place.
- For protos: unused proto files or messages, and duplicated codegen (several language plugins generating code nobody uses).
- Count the totals (external repos fetched for a full build, npm packages) as a baseline.

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
