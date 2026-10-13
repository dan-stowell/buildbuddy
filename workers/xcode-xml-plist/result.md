Replace the Xcode version plist decoder with encoding/xml

Xcode discovery used github.com/groob/plist only to read CFBundleShortVersionString and ProductBuildVersion from Contents/version.plist. This change replaces that general-purpose decoder with a small encoding/xml parser in a platform-neutral file, eliminating the module's last first-party source and BUILD references.

The parser reads the plist dictionary as ordered key/value pairs, handles either field order, and skips entire unknown values, including nested dictionaries and arrays. Missing fields remain empty; malformed pairs, non-string version values, and missing root dictionaries return errors. Existing file-open and decode error wrapping is preserved, and the opened plist file is now closed.

Tests cover a fixture with reordered and unknown keys, nested unknown values, missing fields, wrong field types, malformed pairs, and truncated XML. The portable tests reuse the existing xcode_test target, which can now run on Linux. Existing locator tests retain their Darwin-only source build tag.

Measured changes across server/xcode Go files and BUILD:

| Metric | Before | After |
| --- | ---: | ---: |
| Library/test targets | 2 | 2 |
| Unique direct dependency labels | 6 | 5 |
| Unique external dependency labels | 2 | 1 |
| Go and BUILD lines | 434 | 565 |
| First-party groob/plist imports | 1 | 0 |

Shared dependency files are intentionally unchanged. github.com/groob/plist can be removed from those declarations in the separate dependency cleanup PR. A repository-wide search of Go and BUILD files finds no remaining references. Binary size was not measured.

Validation:

- PASS: go test ./server/xcode/...
- PASS: bazel test //server/xcode:xcode_test on Linux, including nogo checks.
- PASS: bazel build --config=target-darwin-arm64 //server/xcode:xcode, including nogo checks. Archive symbol inspection confirmed NewXcodeLocator and xcodePlistForPath were compiled, verifying the actual cgo Darwin implementation.
- PASS: Darwin vet and test cross-compilation of the platform-neutral parser and its tests.
- PASS: git diff --check.

The plain GOOS=darwin go vet ./server/xcode/... command fails on Linux because cross-compilation disables cgo while the existing Darwin tests still reference the locator; the configured Bazel Darwin build above provides the full-package cross-compile instead. Native Darwin runtime tests were not run.

Broader verification was limited by VM infrastructure: the repository-wide direct-rdeps query timed out building the unrelated default_execution_image Docker repository, and the scoped query failed with disk exhaustion. A build of the two direct consumers, executor_lib and rbetest, failed at //proto:failure_details_go_proto with compiler exit 1 while only 2.4 MB remained free; the compiler supplied no further diagnostic. Those consumer builds are not fully verified. Cache cleanup allowed the final package test and Darwin build to pass.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3