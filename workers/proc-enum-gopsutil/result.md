# Process enumeration migration

Re-verified the premise before editing: commandutil and procstats were the only first-party Go imports and BUILD dependency edges to mitchellh/go-ps. gopsutil was already used by procstats and commandutil's Windows implementation. The migration removes both go-ps edges without adding an external module.

Both enumeration sites now call gopsutil `Processes()` and `Ppid()`. Enumeration errors still propagate; individual parent-lookup errors are skipped, preserving the existing handling of disappearing or inaccessible processes. PID values are converted back to the existing int-based APIs. Direct-child filtering and recursive tree traversal remain unchanged.

Updated both BUILD files. Added Linux regression tests using synthetic procfs records for direct children versus grandchildren, tree membership, missing process stat files, permission-denied stat files, and enumeration failure. Live child processes supply distinct fixture PIDs even inside Bazel PID namespaces. Permission coverage requires an unprivileged user. procstats_test embeds its library to test the private tree traversal function.

## Measurements

Baseline: `770b88bc89c0b20059d42a237e8c7923ff5b7215`. After: this change. Counts cover the two packages, except the explicitly repository-wide go-ps scans. Dependency counts include all platform branches; external labels are unique across both libraries.

| Metric | Before | After |
| --- | ---: | ---: |
| First-party go-ps Go imports, repository-wide | 2 | 0 |
| First-party go-ps BUILD edges, repository-wide | 2 | 0 |
| go_library/go_test targets in the two BUILD files | 4 | 4 |
| Library dependency edges | 13 | 11 |
| External library dependency edges | 5 | 3 |
| Unique external library dependency labels | 3 | 2 |
| Go LOC, package roots including tests | 1990 | 2109 |
| Go LOC, the two modified production files | 684 | 693 |

The production change adds error handling; the remaining LOC increase is regression coverage. No binary-size or build-time reduction is claimed.

Use the same measurement command for each revision:

```bash
for rev in 770b88bc89c0b20059d42a237e8c7923ff5b7215 HEAD; do
  python3 - "$rev" <<'PY'
import re, subprocess, sys
rev = sys.argv[1]
dirs = ['enterprise/server/remote_execution/commandutil',
        'enterprise/server/util/procstats']
def read(path):
    return subprocess.check_output(['git', 'show', rev + ':' + path], text=True)
builds = [read(d + '/BUILD') for d in dirs]
libraries = '\n'.join(b.split('go_library(', 1)[1].split('\ngo_test(', 1)[0]
                      for b in builds)
labels = re.findall(r'^\s+"((?:@|//)[^"]+)",?$', libraries, re.M)
print(rev, 'targets', sum(len(re.findall(r'^go_(?:library|test)\(', b, re.M))
                         for b in builds))
print('library edges', len(labels), 'external edges',
      sum(l.startswith('@') for l in labels), 'unique external labels',
      len({l for l in labels if l.startswith('@')}))
for pattern, globs in [('@com_github_mitchellh_go_ps//', ['**/BUILD', '**/BUILD.bazel']),
                       ('github.com/mitchellh/go-ps', ['*.go'])]:
    r = subprocess.run(['git', 'grep', '-h', pattern, rev, '--', *globs],
                       capture_output=True, text=True)
    print(pattern, len(r.stdout.splitlines()))
files = subprocess.check_output(['git', 'ls-tree', '-r', '--name-only', rev,
                                 '--', *dirs], text=True).splitlines()
files = [p for p in files if p.endswith('.go') and p.rsplit('/', 1)[0] in dirs]
print('package Go LOC', sum(len(read(p).splitlines()) for p in files))
print('production Go LOC', sum(len(read(p).splitlines()) for p in
      [dirs[0] + '/commandutil.go', dirs[1] + '/procstats.go']))
PY
done
```

## Validation

Used the repository-pinned Bazel 9.3.0rc2 binary at `/tmp/bazel-real`; the installed bazel/bazelisk names point to the BuildBuddy wrapper. Long commands ran under nohup with logs in /tmp and were polled. `--lockfile_mode=off` prevents shared lockfile edits. Tests use local execution with two build jobs and one test at a time.

- `git diff --cached --check`: passed.
- `bazel query --lockfile_mode=off 'rdeps(//..., set(//enterprise/server/remote_execution/commandutil:commandutil //enterprise/server/util/procstats:procstats), 1)'`: failed, exit 7. An unrelated `+dockerfile_image+default_execution_image` repository's Docker build exceeded its timeout. Log: `/tmp/process-rdeps.log`.
- The same query with `--keep_going --nofetch`: returned 25 affected targets, exit 3 due unrelated unfetched repositories. Cross-checked the result against all BUILD references to both packages (17 directories). Log: `/tmp/process-rdeps-cached.log`; target list: `/tmp/process-affected-targets.txt`.
- `bazel query --lockfile_mode=off --keep_going --nofetch 'rdeps(//..., @com_github_mitchellh_go_ps//:go-ps, 1) except @com_github_mitchellh_go_ps//...'`: empty result, exit 3 with the same incomplete external-graph limitation. The repository-wide source/BUILD scans independently show zero first-party users. Log: `/tmp/process-go-ps-users-cached.log`. An earlier unrestricted attempt was interrupted after the first query's Docker timeout to avoid repeating that unrelated repository build.

Package test command:

```bash
/tmp/bazel-real --output_base=/tmp/process-build test \
  --lockfile_mode=off --jobs=2 --local_test_jobs=1 \
  --bes_backend= --remote_executor= --remote_cache= --test_output=errors \
  //enterprise/server/remote_execution/commandutil:commandutil_test \
  //enterprise/server/util/procstats:procstats_test
```

Result: build and static checks completed; exit 3 because one existing CPU-sensitive assertion failed (5.55s measured CPU versus a 5s upper bound). All six procstats tests passed, and both new commandutil regression tests passed, with no skips. Log: `/tmp/process-tests.log`.

The failing `TestRun_EnableStats_ComplexProcessTree_RecordsStatsFromAllChildren` passed all three isolated repetitions with the same command plus `--test_filter=TestRun_EnableStats_ComplexProcessTree_RecordsStatsFromAllChildren --test_arg=-test.count=3`; exit 0. Log: `/tmp/process-cpu-tests.log`. No existing assertion was weakened.

Affected-target and direct-dependent command (25 labels from the query above):

```bash
rg '^//enterprise' /tmp/process-rdeps-cached.log > /tmp/process-affected-targets.txt
/tmp/bazel-real --output_base=/tmp/process-build test \
  --lockfile_mode=off --jobs=2 --local_test_jobs=1 \
  --bes_backend= --remote_executor= --remote_cache= --test_output=errors \
  --target_pattern_file=/tmp/process-affected-targets.txt
```

Initial result: exit 1 during analysis because the explicitly listed sandbox library is macOS-only. Log: `/tmp/process-direct-tests.log`. Retried the same command with `--skip_incompatible_explicit_targets --keep_going`; exit 101 from disk exhaustion. Both changed suites passed (commandutil: 20 cases; procstats: 6 cases; no skips). executor_test, runner_test, remote_execution_test, and bazel_rbe_test failed to build because the VM ran out of disk, with no code errors reported. Log: `/tmp/process-direct-tests-linux.log`. The repository's default tests-local tag filters exclude Docker, bare, secrets, and performance tests.

## Remote verification after disk exhaustion

Per user instruction, no further local Bazel commands were run. Origin fetch used https://github.com/dan-stowell/buildbuddy.git; push remained the internal URL. The initial remote attempt without --config=remote encountered Firecracker image-generation failures requiring root/remote execution; its redundant client was interrupted after the corrected run passed.

Successful command (the target file contains the same 25 labels from the direct-rdeps query):

```bash
bash -lc 'cd ~/repo
mapfile -t process_targets < /tmp/process-affected-targets.txt
bb remote test --config=remote --config=tests-local --lockfile_mode=off \
  --skip_incompatible_explicit_targets --keep_going "${process_targets[@]}"'
```

Result: exit 0; build completed successfully (22,418 actions), 18 build targets and 7 test targets selected; all 7 test targets passed. The macOS-only target and tests excluded by tests-local remain outside Linux verification. Log: /tmp/process-remote-rbe.log. [Remote runner](https://app.buildbuddy.io/invocation/1df622b1-8ea2-48c2-8f8e-cc57a3abff5b); [Bazel invocation](https://app.buildbuddy.io/invocation/893d343c-b5d5-4890-b0d3-15daf9d4bdda).

Package changes are committed as `cf1c74c44`; result.md is excluded from the commit as explicitly requested. No push or PR. Origin fetch URL is restored to https://github.int.exe.xyz/dan-stowell/buildbuddy.git after committing.

## Dependency cleanup and scope

`github.com/mitchellh/go-ps` now has no first-party source imports or BUILD edges. Leave its go.mod/go.sum and Bazel module declarations for the separate serialized dependency-removal PR. No shared dependency files were edited.

The only file outside the two permitted packages is this explicitly requested result.md. No push or PR. Windows/macOS runtime tests and binary-size measurements were not performed on this Linux VM. Older downloaded Bazel archive-cache entries and 4.48 GB of unused Docker build cache from repository setup were reclaimed for disk space; the user subsequently deleted ~/.cache/bazel after disk exhaustion. All subsequent verification uses bb remote test, as requested.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3