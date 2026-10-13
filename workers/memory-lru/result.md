# Use the repository LRU for memory backends

The memory KV store and metrics collector previously depended directly on HashiCorp's LRU. Both now use `server/util/lru`, removing the repository's last two first-party edges to that external LRU and consolidating cache implementations.

Each value counts as one entry through `SizeFn`, preserving the 5,000-entry KV limit and 50,000-entry metrics limit regardless of value size or type. Reads and overwrites refresh recency; overwrites update entries in place without consuming another slot. The KV store uses the LRU's thread-safe wrapper. The metrics collector retains its existing mutex around cache access and compound value updates. Existing deletion, expiration, and flush behavior is preserved.

BUILD dependencies were updated in both packages. Added tests cover capacity, eviction after reads and updates, deletion freeing capacity, empty and variable-sized KV values, mixed metrics value types sharing the same limit, and concurrent access. Concurrent metrics tests check exact counter totals, set membership, and list length. The new tests passed against the original implementation before migration as well as the replacement.

## Dependency impact

| Measurement | Before | After |
| --- | ---: | ---: |
| Backend BUILD targets | 4 | 4 |
| Direct backend library dependency edges | 4 | 4 |
| Direct HashiCorp LRU edges | 2 | 0 |
| Production Go LOC | 365 | 373 |
| Combined backend transitive dependency labels, including source files | 1,641 | 1,996 |
| External labels in that closure | 1,604 | 1,953 |
| External repositories in that closure | 21 | 31 |
| HashiCorp LRU labels in that closure | 11 | 0 |

The standalone backend dependency closures grow because the repository LRU imports metrics packages. The verified benefit is eliminating two first-party external LRU edges; no target-count or binary-size reduction is claimed.

Before/after graph counts use the same command, counting unique label lines and external repository prefixes:

```sh
bazel query 'deps(set(//server/backends/memory_kvstore:memory_kvstore //server/backends/memory_metrics_collector:memory_metrics_collector))'
```

The first-party usage search found two Go imports and two BUILD references before, and none after:

```sh
rg -n 'hashicorp/golang-lru|@com_github_hashicorp_golang_lru' --glob '*.go' --glob BUILD --glob BUILD.bazel
```

The external module remains required transitively: `github.com/hashicorp/go-immutable-radix` imports `github.com/hashicorp/golang-lru/simplelru`. No external module became wholly unused. Shared dependency files, including `go.mod` and lockfiles, are unchanged.

## Validation

- **Passed before and after migration:** `go test -mod=readonly -race ./server/backends/memory_kvstore ./server/backends/memory_metrics_collector`.
- **Passed:** `bazel test --jobs=2 --local_test_jobs=2 --remote_executor= --bes_backend= --test_output=errors //server/backends/memory_kvstore:memory_kvstore_test //server/backends/memory_metrics_collector:memory_metrics_collector_test`, including static analysis.
- **Passed:** all seven non-integration direct-dependent tests. The affected-target run analyzed all 14 targets, compiled the three libraries including `server/libmain`, and reported nine passing tests in total. Command: `bazel test --jobs=2 --local_test_jobs=2 --remote_executor= --bes_backend= --test_output=errors --target_pattern_file=/tmp/memory-targets.txt`.
- **Integration validation incomplete:** both integration binaries initially failed linking with `no space left on device` on the 25 GB VM disk. Retrying those two targets with `--jobs=1 --local_test_jobs=1` linked the remote Bazel binary, but workflow's external linker failed with a bus error while disk space remained low.
- **Remote Bazel integration suite failed on VM capacity:** a separate single-job run built and executed `//enterprise/server/test/integration/remote_bazel:remote_bazel_test`, then reported scheduler failures for tasks requiring `milli_cpu=3000, memory_bytes=7500000000`. The VM has two CPUs. These failures do not establish integration correctness; no backend assertion or race-detector failures were observed.
- **Passed:** `git diff --check`.

The requested `bazel query 'rdeps(//..., set(//server/backends/memory_kvstore:memory_kvstore //server/backends/memory_metrics_collector:memory_metrics_collector), 1)'` failed while preloading an unrelated Docker execution-image repository whose build timed out. Direct dependents were instead identified with a successful BUILD-attribute query and cross-checked against BUILD-file searches:

```sh
bazel query 'attr("deps", "//server/backends/memory_(kvstore|metrics_collector)(:|\\])", //...) union set(//server/backends/memory_kvstore:memory_kvstore //server/backends/memory_kvstore:memory_kvstore_test //server/backends/memory_metrics_collector:memory_metrics_collector //server/backends/memory_metrics_collector:memory_metrics_collector_test)'
```

Bazel validation ran locally in background jobs limited to the changed targets and direct dependents. Cache cleanup was necessary to keep compilation running; no repository source was changed for that cleanup. Integration tests, scheduler limits, and shared toolchains were not modified to work around the VM limitations.

Detailed evidence remains in `/tmp/memory-deps-{before,after}.log`, `/tmp/memory-go-{before,after}.log`, `/tmp/memory-bazel-backends.log`, `/tmp/memory-bazel-direct.log`, `/tmp/memory-bazel-integration-retry.log`, `/tmp/memory-bazel-remote-only.log`, and `/tmp/memory-direct.log`.

## PR scope

The branch changes only the two backend packages' Go source, tests, and BUILD files. This write-up is a local, untracked file at `/home/exedev/repo/result.md` and is excluded from the branch commits. No PR has been opened and nothing has been pushed.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3