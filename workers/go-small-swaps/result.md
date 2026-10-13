# Replace external atomic and slices dependencies with the Go standard library

This change removes three direct first-party dependency edges to two external modules while preserving behavior. It updates the affected BUILD files and adds a parser comparison test for rbeperf.

crypter_key_cache uses stdlib sync/atomic.Int32, with Add(1)/Add(-1) replacing Inc/Dec. The zero value and Load behavior are preserved; existing cache refresh concurrency tests exercise the counter.

The rbeperf parser swap is omitted because replacing shellwords.Parse with server/util/shlex.Split would change remote command arguments. A retained 13-case table test compares quotes, adjacent quotes, escapes, empty arguments, empty commands, malformed input, semicolons, and comments. Both parsers reject the tested malformed inputs (error text and partial results differ, but prepareCommands returns on any error). However:

| Input | shellwords.Parse | shlex.Split |
| --- | --- | --- |
| `echo first # comment` | `["echo", "first", "#", "comment"]` | `["echo", "first"]` |
| `echo first; echo second` | `["echo", "first"]` | `["echo", "first;", "echo", "second"]` |

There is no input restriction in prepareCommands that makes these differences irrelevant. The comparison test records these incompatibilities; rbeperf keeps shellwords.Parse.

task_router_test and approxlru_test use stdlib slices for IndexFunc, Reverse, and SortFunc. The repository's Go version supports these APIs; no call-site behavior change is needed. Removed the corresponding BUILD dependencies. Shared dependency files are unchanged; module cleanup remains a separate change.

## Measurements

Measured before any edits and after all edits using the same Python command below. Counts are BUILD target declarations, explicit Go deps label occurrences, external Go deps label occurrences, and all Go source/test lines within each package. Inline embed/base/platform labels are outside this metric. These measure direct edges; they do not estimate binary size or transitive closure savings.

| Package | Targets before → after | Dependency labels before → after | External labels before → after | Go LOC before → after |
| --- | --- | --- | --- | --- |
| enterprise/server/crypter_key_cache | 2 → 2 | 18 → 17 | 4 → 3 | 712 → 712 |
| enterprise/tools/rbeperf | 7 → 8 | 20 → 22 | 9 → 10 | 889 → 933 |
| enterprise/server/scheduling/task_router | 2 → 2 | 28 → 27 | 7 → 6 | 1651 → 1651 |
| server/util/approxlru | 2 → 2 | 9 → 8 | 5 → 4 | 576 → 576 |

Including the comparison test, total external dependency label occurrences decrease from 25 to 23 (three removed, one added).

```python
import pathlib, re, json
paths = ['enterprise/server/crypter_key_cache', 'enterprise/tools/rbeperf',
         'enterprise/server/scheduling/task_router', 'server/util/approxlru']
for d in paths:
    b = pathlib.Path(d + '/BUILD').read_text()
    print(d, json.dumps(dict(
        targets=len(re.findall(r'^    name = ', b, re.M)),
        deps=len(re.findall(r'^        "(?:@|//|:)', b, re.M)),
        external_labels=len(re.findall(r'^        "@', b, re.M)),
        go_loc=sum(len(p.read_text().splitlines()) for p in pathlib.Path(d).glob('*.go')))))
```

## Module users

Same repository-wide command before and after:

```sh
rg -n 'go.uber.org/atomic|github.com/mattn/go-shellwords|golang.org/x/exp/slices|@org_uber_go_atomic|@com_github_mattn_go_shellwords|@org_golang_x_exp//slices' --glob '*.go' --glob BUILD --glob BUILD.bazel
```

Before: Uber atomic had one Go import and one BUILD edge, both in crypter_key_cache. After: no matches for Uber atomic. Thus go.uber.org/atomic has no remaining first-party source/BUILD users in this search; shared dependency declarations stay untouched for the serialized cleanup. shellwords remains in rbeperf production and its comparison test. x/exp/slices remains in enterprise/server/util/cpuset (cpuset.go:17 and BUILD:22); neither module is now unused by first-party code.

## Verification

All long commands were launched with `nohup bash -c '…; echo $? > /tmp/dep-swaps-<name>.exit' > /tmp/dep-swaps-<name>.log 2>&1 < /dev/null &` and polled to completion. Local builds used two jobs, disabled remote execution/cache explicitly, and disabled lockfile updates.

Passed (exit 0), all three tests executed successfully:

```sh
bazel test --jobs=2 --remote_executor= --remote_cache= --lockfile_mode=off \
  //enterprise/tools/rbeperf:rbeperf_test \
  //enterprise/server/scheduling/task_router:task_router_test \
  //server/util/approxlru:approxlru_test
```

Passed (exit 0), race detection available and enabled; one test target executed successfully:

```sh
bazel test --jobs=2 --remote_executor= --remote_cache= --lockfile_mode=off --config=race \
  //enterprise/server/crypter_key_cache:crypter_key_cache_test
```

The prescribed global direct reverse-dependency query was attempted and allowed to finish. It failed with exit 7 because an unrelated Docker repository fetch timed out building `dockerfiles/default_execution_image/Dockerfile`; no package tests failed:

```sh
bazel query 'rdeps(//..., set(//enterprise/server/crypter_key_cache:crypter_key_cache //enterprise/tools/rbeperf:rbeperf_lib //enterprise/server/scheduling/task_router:task_router_test //server/util/approxlru:approxlru_test), 1)'
```

Fallback: searched all BUILD/BUILD.bazel files for references to the changed targets:

```sh
rg -n 'enterprise/server/crypter_key_cache|enterprise/tools/rbeperf:rbeperf_lib|task_router:task_router_test|approxlru:approxlru_test' --glob BUILD --glob BUILD.bazel
```

The crypter_key_cache references were in crypter_service/BUILD:11, :39 and remote_crypter/BUILD:12. Other references were only the cache's own importpath. The rbeperf production library is unchanged; only its new comparison test was added. Confirmed the candidate packages with this scoped query, which passed (exit 0):

```sh
bazel query --lockfile_mode=off 'rdeps(set(//enterprise/server/crypter_key_cache:* //enterprise/server/crypter_service:* //enterprise/server/remote_crypter:* //enterprise/tools/rbeperf:rbeperf_test //enterprise/server/scheduling/task_router:task_router_test //server/util/approxlru:approxlru_test), set(//enterprise/server/crypter_key_cache:crypter_key_cache //enterprise/server/crypter_key_cache:crypter_key_cache_test //enterprise/tools/rbeperf:rbeperf_test //enterprise/server/scheduling/task_router:task_router_test //server/util/approxlru:approxlru_test), 1)'
```

It returned the five changed library/test targets plus:

- //enterprise/server/crypter_service:crypter_service
- //enterprise/server/crypter_service:crypter_service_test
- //enterprise/server/remote_crypter:remote_crypter

Direct-dependent verification passed (exit 0): both library targets built successfully, and crypter_service_test executed successfully:

```sh
bazel test --jobs=2 --remote_executor= --remote_cache= --lockfile_mode=off \
  //enterprise/server/crypter_service:crypter_service \
  //enterprise/server/crypter_service:crypter_service_test \
  //enterprise/server/remote_crypter:remote_crypter
```

The preliminary parser comparison also passed via `cd /tmp/dep-parser-check && go test -v`, using a copy of the repo Split implementation and the exact parser versions pinned in go.mod, in an isolated temporary module. The retained Bazel table test verifies the actual repository package. `git diff --check` passed. No shared dependency files were changed.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3