# Remove unused Go resource bundles

The Go wrappers `//config:bundle`, `//enterprise/config:bundle`, and `//enterprise:bundle` have no first-party users. Delete their three `bundle.go` files and `go_library` rules, including the unused loads. Configs and licenses continue to ship through the existing filegroups, binary data, and packaging targets. Every filegroup and exported-file declaration is preserved.

The change is split into three commits, one per bundle. No shared dependency or lockfiles changed, and no external module becomes unused: the wrappers imported only the Go standard library and the still-used first-party `fileresolver` package. No binary-size reduction is claimed for packages that were already unused.

## Validation

All three required binaries built successfully with `bb remote` (exit 0), and their artifacts downloaded successfully:

```sh
bash -lc 'cd ~/repo && bb remote build //server/cmd/buildbuddy:buildbuddy //enterprise/server/cmd/server:buildbuddy //enterprise/server/cmd/executor:executor'
```

[Successful binary build](https://app.buildbuddy.io/invocation/c888d29c-652c-48fe-834e-226ead414f30).

A separate `bb remote --script` invocation built the same binaries plus `//enterprise/server/cmd/server:config_files_tar` and `//enterprise/server/cmd/server:runfiles_tar`, using `--remote_download_outputs=all`. Python assertions inspected the actual generated runfiles directories (`buildbuddy_/buildbuddy.runfiles` and `executor_/executor.runfiles`) and tar members, comparing each resource's bytes with its source. All checks passed (exit 0):

- OSS server: all five OSS YAML configs remain in actual runfiles.
- Enterprise server and executor: all ten enterprise YAML configs remain in each binary's actual runfiles.
- Executor: `enterprise/LICENSE` remains in actual runfiles.
- Enterprise server: its config tar contains all ten configs; its runfiles tar contains `enterprise/LICENSE`.

[Successful resource build](https://app.buildbuddy.io/invocation/f4340d6c-2897-4542-b63b-68633fd1e943) and [passing runfiles assertions](https://app.buildbuddy.io/invocation/2120d17e-0c4b-436d-a7df-4d8703dc9372). The remote checker and complete logs are in `/tmp/bundle-remote-resources.sh` and `/tmp/bundle-bb-remote-resources-retry.log`; the binary log is `/tmp/bundle-bb-remote-retry.log`.

These completed remote builds supersede the earlier local attempt that exhausted the VM disk. No local Bazel commands were used for this follow-up. An initial remote build also succeeded, but its local artifact download failed while the VM was full; the successful rerun above resolved that.

Before deletion, a source/BUILD search found no imports or target references. `bazel query --bes_backend= --output=xml //...` succeeded before and after; inspecting all rule inputs found no users of the bundles, and the rule-set difference is exactly the three deleted labels. The broad `rdeps(//..., ...)` query initially failed on a Docker repository-fetch timeout; retries with `--keep_going --nofetch` returned empty results with missing-external-repository warnings. The successful complete first-party XML audit independently establishes the no-users premise.

## Measured change

The same XML query and rule/input counting code were used before and after. LOC counts use `splitlines()` on the same three wrapper and BUILD paths, counting deleted files as zero.

| Measurement | Before | After |
| --- | ---: | ---: |
| First-party rules | 5,043 | 5,040 |
| First-party Go library rules | 693 | 690 |
| Wrapper Go packages | 3 | 0 |
| Unique first-party rule-input labels | 6,605 | 6,602 |
| External rule-input labels | 1,121 | 1,121 |
| Wrapper Go LOC | 49 | 0 |
| Three touched BUILD files LOC | 71 | 35 |

No new tests were added for deleting unused wrappers; the required binary builds and resource assertions passed. `git diff --check` passes. No full-repository build/test, push, or PR was performed.

## PR preparation

Every branch commit was checked for `result.md` at any path: none contains one. `git diff --name-only origin/master...HEAD` lists only the six intended code/BUILD files. This root `result.md` remains on disk and is untracked (ignored by the repository). Origin's fetch and push URLs are restored to `https://github.int.exe.xyz/dan-stowell/buildbuddy.git`.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3