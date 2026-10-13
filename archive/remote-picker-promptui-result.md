# Remote-Bazel picker: Survey → Promptui

## Change and premise

Before editing, `rg -n 'AlecAivazis/survey|promptui' --glob '*.go' --glob BUILD` found Survey used only by `cli/remotebazel`; Promptui was already used by `cli/add` and `cli/picker`. The baseline Bazel dependency query confirmed Survey, go-shellquote, and mgutz/ansi in the remote-Bazel library graph. The dependency-reduction premise holds.

Replaced Survey Select/AskOne with Promptui Select/Run. Kept the prompt text, seven-row list, case-insensitive substring search (including URLs containing `/`), stdin input and stderr prompt output, single-remote shortcut, cached-remote lookup, stale-cache fallback, wrapped selection errors, and warning-only handling of cache-write failures. Map the returned index directly to the configured remote. BUILD now uses the existing Promptui repository. Search starts enabled; Ctrl-S toggles it so `/` stays available for URL filtering, while arrow/page keys keep Promptui defaults.

Added subprocess tests with a pseudo-terminal for default selection, arrow selection, typed name/URL search, stale cache, Ctrl-C without caching, cache reuse, and restoration of terminal state. Added a non-terminal EOF test. PTY subprocesses put stdin/stderr on a terminal while capturing stdout separately, testing redirected stdout and ensuring prompts stay on stderr. Existing Git configuration tests remain.

## Measurements

Both measurements used `bazel query 'deps(//cli/remotebazel:remotebazel)'`, with baseline commit `770b88bc89c0b20059d42a237e8c7923ff5b7215` BUILD dependency before replacement and the updated BUILD afterward. Counts include source-file and metadata labels, not just rules. Parse unique stdout lines starting with `//` or `@` (exclude log/progress lines).

| Metric | Before | After | Delta |
| --- | ---: | ---: | ---: |
| Library transitive labels | 5,967 | 5,918 | -49 |
| Library external labels | 5,521 | 5,472 | -49 |
| Local go_library/go_test targets | 2 | 2 | 0 |
| Library direct BUILD deps | 34 | 34 | 0 |
| Test direct BUILD deps | 17 | 18 | +1 |
| External BUILD dependency entries | 9 | 10 | +1 |
| Production Go LOC | 1,755 | 1,763 | +8 |
| Test Go LOC | 1,128 | 1,219 | +91 |

The additional test dependency is already-present `golang.org/x/term` for portable terminal-state assertions. Local target/dep counts use the same Python regexes on baseline and updated BUILD: `^go_(?:library|test)\(` for targets, quoted labels inside each `deps = [...]` for direct deps, and quoted `@` labels inside those arrays for external entries. LOC uses `wc -l` on each Go file for both versions (`git show 770b88bc89c0b20059d42a237e8c7923ff5b7215:<path>` supplies the baseline).

Three repositories leave this library graph: Survey, go-shellquote, and mgutz/ansi. Two enter: Promptui and readline, both already used by other CLI packages. No module declaration was removed in this change; the whole-module gain awaits the separate dependency-removal PR. Binary size was not measured.

## Validation

Long commands ran under `nohup ... > /tmp/remotebazel-*.log 2>&1 < /dev/null &` and were polled.

- Baseline and updated `bazel query 'deps(//cli/remotebazel:remotebazel)'`: passed.
- `bazel query 'rdeps(//..., //cli/remotebazel:remotebazel, 1)'`: interrupted after loading 2,648 packages. It triggered builds of unrelated default-execution and executor Docker images and reduced free disk space to 4.3 GB on this 25 GB VM. No test/build was abandoned for being slow. Subsequently `docker builder prune --force` cleared 4.127 GB of inactive build cache left by image setup, restoring roughly 7.5 GB free for the scoped build. Later, removing only downloaded archive files larger than 10 MiB from Bazel's content-addressable repository cache reclaimed another 1,797 MiB; extracted repositories and compiled outputs were retained:
  `find /home/exedev/.cache/bazel/_bazel_exedev/cache/repos/v1/content_addressable -type f -name file -size +10M -delete`. Finally, the interrupted full-repository query had downloaded Chromium for macOS ARM64/x64 and Windows x64; removing those unused platform browser cache directories reclaimed roughly another 1 GB before linking. As disk pressure increased before linking, the remaining unused Linux Chromium and driver caches were also removed. After the build failed, 439 MiB of reusable sandbox scratch data was cleared; subsequent commands disable sandbox-directory reuse and use one build job. Compiled outputs were retained.
- Narrowed query `bazel query 'rdeps(//cli/... + //enterprise/server/test/integration/remote_bazel:all, //cli/remotebazel:remotebazel, 1)'`: passed, returning the library, its test, `//cli/cli_command/register:register`, and `//enterprise/server/test/integration/remote_bazel:remote_bazel_test`. Repository-wide `rg -n '"//cli/remotebazel' --glob BUILD --glob BUILD.bazel` found exactly the two external users included in this narrowed universe: CLI command registration and the remote-Bazel integration test.
- `bazel test --jobs=2 --local_test_jobs=1 --remote_executor= --bes_backend= --lockfile_mode=off //cli/remotebazel:all //cli/cli_command/register:register //enterprise/server/test/integration/remote_bazel:remote_bazel_test`: failed after 7,151 seconds because disk filled while nogo wrote `enterprise/server/remote_execution/filecache/filecache.facts`; both tests were skipped. Almost all dependency compilation completed and was cached.
- `bazel test --jobs=1 --local_test_jobs=1 --noreuse_sandbox_directories --remote_executor= --bes_backend= --lockfile_mode=off //cli/remotebazel:all`: passed all tests and nogo checks in 56 seconds after adding the URL-search key mapping and regression test. All seven PTY cases and the non-terminal EOF case passed.
- `bazel test --jobs=1 --local_test_jobs=1 --noreuse_sandbox_directories --remote_executor= --bes_backend= --lockfile_mode=off //cli/cli_command/register:register //enterprise/server/test/integration/remote_bazel:remote_bazel_test`: all targets compiled and nogo checks passed; the integration test failed all seven shards because the local scheduler could not fit hosted-runner requests of `milli_cpu=3000, memory_bytes=7500000000` on this two-CPU VM. The error was `no registered executors ... can fit a task`; every shard logged executor resource rejections and none logged disk errors. This fails before remote execution, independently of the picker.
- `git diff --check`: passed.

The picker command was repeated after removing the redundant trailing colon from the Label (Promptui supplies its own colon): passed in 9.354 seconds, including all tests and nogo checks.

## Dependencies and exclusions

Shared dependency files were not edited. Survey, go-shellquote, and mgutz/ansi now have no first-party Go or BUILD users: `rg -n 'AlecAivazis/survey|kballard/go-shellquote|mgutz/ansi' --glob '*.go' --glob BUILD` produced no matches (exit 1). The baseline/updated dependency queries also show all three repositories disappearing from the affected graph. Survey's generated external BUILD depended on go-shellquote, and its core BUILD depended on mgutz/ansi. A generated-BUILD scan also ran:

```sh
rg -n -L '"@com_github_(mgutz_ansi|kballard_go_shellquote)//' --glob BUILD.bazel /home/exedev/.cache/bazel/_bazel_exedev/71099934f9d3af2730aa81dc5fd03b9c/external/gazelle++go_deps+*
```

It found exactly two edges: Survey's root BUILD → go-shellquote and Survey's core BUILD → mgutz/ansi. No other fetched module BUILD referenced either. Together with the first-party scan, this supports removing all three modules in the separate dependency PR. Dependency declarations remain for the separate serialized removal PR. Executor capacity and integration-test scheduling code were not changed outside scope to force the end-to-end suite to pass. No push or PR. The report is in `cli/remotebazel/result.md` to keep every tracked edit within scope.

