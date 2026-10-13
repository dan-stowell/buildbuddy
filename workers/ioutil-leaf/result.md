# Decouple ioutil from server/interfaces

`server/util/ioutil` imported `server/interfaces` only to use `Committer` and
`CommittedWriteCloser`, pulling a large dependency closure into a small utility.
Define equivalent local interfaces and remove the interfaces dependency from
BUILD. Existing writers and callers remain compatible through Go structural
typing: the method sets and `CustomCommitWriteCloser` return type are unchanged.
No exported function returned `interfaces.CommittedWriteCloser`, so no type
alias or caller changes are necessary.

Add a regression test covering underlying commit forwarding, callback order,
written byte counts, commit errors, rejection of repeated commits, and close
forwarding. Runtime behavior is unchanged.

## Dependency reduction

Measured before and after with `bazel query 'deps(//server/util/ioutil)' | wc -l`
(query output captured to files, then counted):

| Measurement | Before | After |
| --- | ---: | ---: |
| Total dependency labels, including sources and implicit dependencies | 4,576 | 1,630 |
| First-party labels (`//`) | 289 | 37 |
| External labels (`@`) | 4,287 | 1,593 |
| First-party rules, including implicit analyzers | 187 | 32 |
| First-party rules excluding unchanged analyzer/config rules | 158 | 3 |
| ioutil.go lines | 446 | 457 |
| BUILD lines | 27 | 26 |
| ioutil_test.go lines | 275 | 333 |

The 29 unchanged implicit rules are `//:nogo_config`, `//:vet`, `//:vet_actual`,
and 26 `//rules/go/analyzer` rules. Excluding these confirms the survey's
158 → 3 estimate; the remaining rules are ioutil, bytebufferpool, and status.
Rule counts use `kind(".* rule", ...)`; baseline first-party labels were saved
before editing and classified afterward. The after query was
`kind(".* rule", deps(//server/util/ioutil)) intersect //...`.

## Validation

- `gofmt` and `git diff --check`: passed.
- `bazel test --jobs=2 //server/util/ioutil:ioutil_test`: passed, including
  required nogo validation (1 test target; 3,566 actions).
- `bazel build --jobs=2 --target_pattern_file=/tmp/ioutil-build-targets.txt`:
  passed for ioutil and all 31 direct dependents, including dependent test
  binaries and required nogo validation (32 targets; 4,012 executed actions).
  Dependent tests were built but not run.

The requested `bazel query 'rdeps(//..., //server/util/ioutil, 1)'` failed
while fetching the unrelated `+dockerfile_image+default_execution_image`
repository: its Docker build timed out. Instead,
`bazel query 'attr("deps", "//server/util/ioutil(:ioutil)?", //...)'` succeeded
and found 31 direct dependents, matching all 31 BUILD references found by
`rg -n 'server/util/ioutil' --glob BUILD`. All references are deps entries.
The build target file contains those 31 targets plus ioutil itself.

## Scope

The PR changes only `server/util/ioutil/ioutil.go`, `ioutil_test.go`, and BUILD.
No callers, shared dependency files, lockfiles, or interfaces definitions change.
No external module becomes unused: 198 remaining BUILD files directly reference
`//server/interfaces`, as verified by
`rg -l '"//server/interfaces"' --glob BUILD server enterprise | wc -l`.
Its dependency closure therefore remains used elsewhere. No external dependency
cleanup or unrelated Docker repository fix is included.

This report remains on disk only, untracked and excluded from branch history.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3