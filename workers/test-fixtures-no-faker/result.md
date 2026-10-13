# Replace faker with deterministic test fixtures

The proto utility tests and ClickHouse invocation conversion test populated their
inputs with faker. Replace those inputs with explicit fixtures and remove both
faker BUILD dependencies. This leaves `github.com/go-faker/faker/v4` with no
first-party users, making the test-only module eligible for separate removal.

The proto fixtures populate every field of the five tested message types,
including nested and repeated messages. Descriptor reflection requires all fields
to be populated, so adding a proto field requires updating its fixture. Size tests
and benchmarks also include empty messages, and benchmark selection is deterministic.

The invocation fixture assigns nonzero values to every compared field, with
distinct numeric and string values. The existing reflection comparison remains,
with a nonzero assertion to catch incomplete fixture updates. UUID encoding and
tag splitting have explicit expected values. Existing fallback, unknown-field,
and external VT message tests remain.

## Dependency impact

| Metric across the two tests and their BUILD files | Before | After |
| --- | ---: | ---: |
| Test targets | 2 | 2 |
| Direct test dependency edges | 14 | 14 |
| Unique direct external labels | 7 | 8 |
| Unique direct external repositories | 4 | 3 |
| Faker BUILD edges | 2 | 0 |
| Faker Go imports | 2 | 0 |
| LOC | 629 | 729 |

The added labels are protobuf text-format and reflection packages from an existing
module. Shared dependency manifests and lockfiles remain unchanged for the
separate serialized cleanup PR. No production binary-size reduction is claimed.
Benchmark timings are not directly comparable with the old 30 generated samples:
the new corpus has one empty and one fully populated message per type.

Measurements used the same script for both revisions:

```sh
REV=770b88bc89c0b20059d42a237e8c7923ff5b7215 python /tmp/faker-metrics.py
python /tmp/faker-metrics.py
```

The premise was checked before editing. The following search finds four baseline
matches (two imports and two BUILD edges) and zero working-tree matches:

```sh
git grep -n -E 'github.com/go-faker/faker/v4|com_github_go_faker_faker_v4' 770b88bc89c0b20059d42a237e8c7923ff5b7215 -- '*.go' '**/BUILD' '**/BUILD.bazel'
git grep -n -E 'github.com/go-faker/faker/v4|com_github_go_faker_faker_v4' -- '*.go' '**/BUILD' '**/BUILD.bazel'
```

A full search, `rg -n 'go-faker|com_github_go_faker' --glob '!result.md'`, finds only
`go.mod`, `go.sum`, and `deps/go_deps.MODULE.bazel`. The retained module version is
`v4.0.0-beta.3`.

## Validation

- `bazel test //server/util/proto:proto_test //server/util/clickhouse/schema:schema_test`
  passed, including static analysis: both test targets pass. The final run used
  the cached proto result and reran schema. Log: `/tmp/faker-test-final.log`.
- `bazel query 'labels(deps, set(//server/util/proto:proto_test //server/util/clickhouse/schema:schema_test))'`
  passed: 13 unique direct labels, with no faker label.
- `bazel query 'rdeps(set(//server/util/proto:all //server/util/clickhouse/schema:all), set(//server/util/proto:proto_test //server/util/clickhouse/schema:schema_test), 1)'`
  passed: only the two changed tests were returned.
- `bazel query 'labels(tests, kind(test_suite, //...)) intersect set(//server/util/proto:proto_test //server/util/clickhouse/schema:schema_test)'`
  passed with no test-suite users. A BUILD-file search also found no explicit
  references to either test label elsewhere.
- `gofmt` and `git diff --check` passed.

Full-universe `rdeps(//..., set(//server/util/proto:proto_test //server/util/clickhouse/schema:schema_test), 1)`
verification could not complete: the initial query hit an unrelated
`default_execution_image` Docker repository timeout; a retry with
`--notool_deps --noimplicit_deps` exhausted temporary disk space while downloading
macOS Bazel 9.2.0. Logs: `/tmp/faker-rdeps.log` and `/tmp/faker-rdeps-explicit.log`.
The scoped query, suite-user query, and reference search found no additional direct
reverse dependencies to test. Long Bazel commands ran under `nohup` and were polled.

The PR diff contains only the two test files and their BUILD files. This report is
kept locally, untracked and excluded from branch history. No shared manifest edits,
push, or PR creation were performed.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3