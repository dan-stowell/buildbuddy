# Remove the disabled CLI YAML translator

Delete `cli/translate/yaml`, including its library, test, and BUILD file, and remove the stale “Fix and re-enable” registration comments from the translate registry. YAML translation was already disabled, so CLI behavior is preserved. The registry has no YAML import or BUILD dependency to remove.

Before deletion, the repository-wide reference scan found no imports or target references outside the package's own BUILD file. Its library's only declared consumer was its own test. Bazel confirmed the package contained exactly two targets.

## Measured reduction

The same commands were run before and after the change. Counts cover `cli/translate`, not the full CLI binary.

| Measurement | Before | After | Reduction |
| --- | ---: | ---: | ---: |
| Targets: `bazel query '//cli/translate/...'` | 6 | 4 | 2 |
| Packages: BUILD files under `cli/translate` | 4 | 3 | 1 |
| Go lines: sum of `splitlines()` for `cli/translate/**/*.go` | 762 | 357 | 405 |
| Unique labels: `bazel query 'deps(//cli/translate/...)'` | 5,952 | 1,404 | 4,548 |
| External labels: labels starting with `@` in that query | 5,566 | 1,364 | 4,202 |

The deleted package contained 402 Go lines; the remaining three deleted Go lines were registry comments. Total deletion, including BUILD, is 428 lines. Dependency counts include source files and implicit dependencies and do not measure binary-size or repository-wide dependency reductions. Both dependency queries passed.

## Validation

Using Bazel 9.3.0rc2, matching `.bazelversion`:

- `bazel --batch build //cli/... --jobs=2 --remote_executor= --lockfile_mode=off` — passed, 142 targets (about 74 minutes).
- `bazel --batch test //cli/translate/... --jobs=2 --remote_executor= --lockfile_mode=off` — passed, 1 of 1 test (`//cli/translate/js:js_test`).
- `bazel --batch query 'rdeps(//cli/..., //cli/translate:translate, 1)' --lockfile_mode=off` — passed; the registry's only direct consumer is `//cli/fix:fix`, covered by the CLI build.
- `git diff --check` — passed.
- `rg -n 'cli/translate/yaml|Fix and re-enable' cli` — no remaining matches.

The repository-wide query `bazel query 'rdeps(//..., //cli/translate/yaml:all, 1)'` was attempted before deletion but did not complete: unrelated repository rules triggered Debian snapshot 404s and Docker image builds. It was stopped with SIGTERM (exit 143). The repository-wide source/reference scan and declared BUILD consumers independently support the dead-code premise.

Checks ran in the background and were polled to completion, using the downloaded Bazel binary directly to bypass unreliable detached BuildBuddy launcher startup. `--lockfile_mode=off` kept the shared lockfile unchanged. Unused Docker builder cache and large Bazel download-cache archives were reclaimed to provide disk space; source files, extracted dependencies, and build outputs were retained.

## Dependency impact

No external module becomes unused. `gopkg.in/yaml.v2` remains imported by `cli/config`, `cli/plugin`, and several enterprise packages. Evidence: `rg -n 'gopkg.in/yaml.v2' --glob '*.go'` and `rg -n '@in_gopkg_yaml_v2' cli/config/BUILD cli/plugin/BUILD`; both BUILD files still declare `@in_gopkg_yaml_v2//:yaml_v2`. `testify/assert` remains used by the JS translator test and CLI plugin tests.

Shared dependency files are unchanged. No replacement tests were needed for this disabled dead-code deletion. The PR diff contains only the registry Go file and the three deleted YAML package files. This write-up is retained locally and is not committed. No push or PR was performed.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3