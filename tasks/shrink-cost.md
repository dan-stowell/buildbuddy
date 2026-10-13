# Task (survey, no PR): where build time and binary size go
## Your area
Measure cost; you don't need to find dead code (another survey does that).
- **Clean build:** run `bazel build` for the main server/executor/app targets with `--disk_cache= --remote_cache=` (actually clean), using `--profile` / `--generate_json_trace_profile` and `--execution_log_json_file`. Report wall time, action counts by mnemonic, the critical path (`bazel analyze-profile`), and the top 30 slowest actions.
- **Analysis/fetch phase:** time spent fetching externals and in loading/analysis. Which repos are the slowest to fetch or largest on disk (`$(bazel info output_base)/external`)?
- **Heavy hitters:** cgo/C++ compiles, protoc and codegen, JS bundling, OCI image layers, and big generated files.
- **Binary size:** for the main Go binaries (server, enterprise server, executor, CLI if present), report size and break it down by package/module (`go tool nm -size -sort size`, or `goweight`/`bloaty` if you can install them). Which deps dominate? Which could be dropped or put behind build tags?
- Record the exact commands so they can be rerun after changes to measure progress. Put them in a `## Benchmark recipe` section of result.md.

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
