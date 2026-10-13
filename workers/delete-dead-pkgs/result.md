# Removed unused pubsub fake and RBE execution-ID helper

Deleted both complete packages, including their BUILD files, after successful remote verification of zero reverse dependencies and zero consuming source references:

- `405108204f9547d4ad95a89af8572c1da6b6a83f`: Remove unused pubsub test fake.
- `5bdb4043b7640d19337adc16962b79dcf741e1ba`: Remove obsolete RBE execution ID helper.

Only the four files in the authorized directories are changed in the commits. No shared dependency files were edited. Both deletions remove dead code; no behavior or tests needed adjustment. This report is intentionally uncommitted, as requested, and remains excluded by the existing `.git/info/exclude`. No push or PR was performed.

The origin fetch/push URLs were changed as instructed. All verification in the retry used `bash -lc 'cd ~/repo && bb remote ...'`, so the entire Bazel invocation ran remotely with the configured login-shell credentials. Long commands were launched using `nohup`, logged under `/tmp`, and polled.

## Before/after measurements

The same command, `python3 /tmp/dead-package-metrics.py`, was run before and after deletion. Results are saved in `/tmp/dead-remote-metrics.before` and `/tmp/dead-remote-metrics.after`. It counts declared library rules, directories containing BUILD files, direct `deps` labels, external labels within `deps`, and physical Go lines. The measured definitions match the complete contents of these two packages.

| Package | Targets before → after | Packages before → after | Direct deps before → after | Direct external labels before → after | Go LOC before → after |
| --- | --- | --- | --- | --- | --- |
| `server/testutil/pubsub` | 1 → 0 | 1 → 0 | 2 → 0 | 0 → 0 | 92 → 0 |
| `enterprise/server/util/rbeutil` | 1 → 0 | 1 → 0 | 0 → 0 | 0 → 0 | 25 → 0 |
| Total | 2 → 0 | 2 → 0 | 2 → 0 | 0 → 0 | 117 → 0 |

Gain: **2 targets, 2 packages, 117 Go lines**, plus 21 BUILD lines (138 deleted lines overall). No binary-size reduction is claimed: neither package had consumers.

Reproduce the measurement command using this script:

```python
from pathlib import Path
import re

for directory in ("server/testutil/pubsub", "enterprise/server/util/rbeutil"):
    p = Path(directory)
    builds = [f for f in p.glob("BUILD*") if f.is_file()]
    content = "\n".join(f.read_text() for f in builds)
    deps = re.findall(r'"(//[^"\n]+|@[^"\n]+)"',
                      "\n".join(re.findall(r'deps\s*=\s*\[(.*?)\]', content, re.S)))
    print(directory,
          len(re.findall(r"^go_library\(", content, re.M)),
          int(bool(builds)), len(deps),
          len([d for d in deps if d.startswith("@")]),
          sum(len(f.read_text().splitlines()) for f in p.glob("*.go")))
```

## Source and dependency evidence

Before deletion, this search returned no matches (exit 1):

```sh
rg -n 'server/testutil/pubsub|enterprise/server/util/rbeutil|NewTestPubSub|TestSubscriber|IsV2ExecutionID|V2ExecutionIDPrefix' \
  --glob '!server/testutil/pubsub/**' \
  --glob '!enterprise/server/util/rbeutil/**' --glob '!result.md' .
```

A separate search for `TestPubSub` found only the unrelated test function in `enterprise/server/server_notification/server_notification_test.go`; it tests the production pubsub implementation and does not import the fake.

After deletion, the corresponding source/BUILD/bzl search again returned no matches:

```sh
rg -n 'server/testutil/pubsub|enterprise/server/util/rbeutil|NewTestPubSub|TestSubscriber|IsV2ExecutionID|V2ExecutionIDPrefix' \
  --glob '*.go' --glob '*BUILD*' --glob '*.bzl' --glob '!bb-out/**' .
```

This remote query passed (exit 0) before deletion:

```sh
bb remote query --lockfile_mode=off \
  'labels(deps, set(//server/testutil/pubsub:pubsub //enterprise/server/util/rbeutil:rbeutil))'
```

It returned only `//server/interfaces:interfaces` and `//server/util/log:log`. The helper imported standard-library `strings`; the fake imported standard-library `context`/`sync` and those two internal packages. **No external Go module became unused.** The BUILD load of `@io_bazel_rules_go//go:def.bzl` remains used by 529 retained BUILD files under `server/` and `enterprise/`, verified with:

```sh
rg -l 'load\("@io_bazel_rules_go//go:def.bzl"' --glob BUILD server enterprise | wc -l
```

No external dependency cleanup was attempted; `go.mod`, `go.sum`, Bazel module/dependency files, and lockfiles were left alone.

## Successful remote verification

The default remote runner had no Docker daemon, so the initial full rdeps attempts failed with exit 7 while fetching an unrelated Docker test-image repository. BuildBuddy's documented `init-dockerd=true` runner property resolved that blocker without changing repository files. Both full queries then completed with exit 0 and `INFO: Empty results`:

```sh
bb remote --runner_exec_properties=init-dockerd=true query --lockfile_mode=off \
  'rdeps(//..., //server/testutil/pubsub:pubsub) except //server/testutil/pubsub:pubsub'
bb remote --runner_exec_properties=init-dockerd=true query --lockfile_mode=off \
  'rdeps(//..., //enterprise/server/util/rbeutil:rbeutil) except //enterprise/server/util/rbeutil:rbeutil'
```

- [Pubsub full rdeps](https://app.buildbuddy.io/invocation/cfe133ec-e2aa-441b-838b-3af4b7e4e764): empty; log `/tmp/dead-remote-pubsub.docker.log`.
- [RBE helper full rdeps](https://app.buildbuddy.io/invocation/4501b751-2c07-4040-b0ef-cae41f24f26c): empty; log `/tmp/dead-remote-rbeutil.docker.log`.

The explicit direct-rdeps check also passed with empty results (exit 0):

```sh
bb remote --runner_exec_properties=init-dockerd=true query --lockfile_mode=off \
  'rdeps(//..., set(//server/testutil/pubsub:pubsub //enterprise/server/util/rbeutil:rbeutil), 1) except set(//server/testutil/pubsub:pubsub //enterprise/server/util/rbeutil:rbeutil)'
```

[Direct-rdeps invocation](https://app.buildbuddy.io/invocation/d0414c34-593b-4d69-86d3-905122becd60); log `/tmp/dead-remote-direct-rdeps.before.log`. There were therefore no consumer tests or additional direct-rdep targets to build.

After deletion, this query passed with empty results (exit 0), confirming no remaining labels in either package:

```sh
bb remote query --lockfile_mode=off \
  'filter("^//(server/testutil/pubsub|enterprise/server/util/rbeutil)(:|/)", //...)'
```

[Post-deletion target query](https://app.buildbuddy.io/invocation/0f931a1f-f7c2-451b-ba6a-95da2b00eaf2); log `/tmp/dead-remote-targets.after.log`. The remote log confirms all four local deletion patches were applied. Queries referencing the deleted target labels directly would now fail, so the original full rdeps queries were performed before deletion.

The same remote build command was run before and after deletion:

```sh
bb remote build --lockfile_mode=off \
  //enterprise/server/cmd/executor:executor \
  //enterprise/server/cmd/server:buildbuddy
```

- Before: **passed (exit 0)**, [invocation](https://app.buildbuddy.io/invocation/af4de3b0-2512-4267-bf50-3f487becae24); log `/tmp/dead-remote-build.before.log`.
- After: **passed (exit 0)**, [invocation](https://app.buildbuddy.io/invocation/66eb8ce6-7963-49cb-b333-c91caf8e6666); log `/tmp/dead-remote-build.after.log`. The remote log confirms all four deletion patches were applied before the build.

No whole-workspace build/test was run. No tests were added or run: the packages had no consumers, including test consumers. `git diff --check 770b88bc8..HEAD` passed; the diff contains only the four scoped deletions.

## Earlier local attempts

These occurred before the instruction to use Remote Bazel. Their failures were not treated as evidence of zero consumers, and no deletion was made until remote verification succeeded.

- Full `bazel query 'rdeps(//..., //server/testutil/pubsub:pubsub) except //server/testutil/pubsub:pubsub'`: exit 7, unrelated `default_execution_image` Docker build timed out after loading 3,421 packages. Log `/tmp/pubsub-rdeps.err`.
- `bazelisk query --lockfile_mode=off` for each individual full rdeps expression: interrupted (exit 8); the helper query was waiting for the output-base lock. Logs `/tmp/pubsub-direct.err` and `/tmp/rbeutil-rdeps.err`.
- Direct downloaded-Bazel retries with `--lockfile_mode=off --noimplicit_deps --notool_deps --bes_backend=` for pubsub, and with `--keep_going` for the combined full rdeps expression: interrupted (exit 8), repeated repository setup and Debian snapshot HTTP 404s. Logs `/tmp/pubsub-filtered.err`, `/tmp/dead-rdeps.partial.err`.
- Local build of the two binaries with `--lockfile_mode=off --jobs=2 --remote_executor= --bes_backend=`: interrupted (exit 8) to continue with cache acceleration. Log `/tmp/dead-build.log`.
- Same direct downloaded-Bazel build plus `--remote_cache=grpcs://remote.buildbuddy.io --remote_upload_local_results=false`: interrupted (exit 8) after cache authentication reported a missing API key. Log `/tmp/dead-build-cache.log`.
- Same cache build via the installed authenticated wrapper: interrupted (exit 8) to increase cache-download concurrency. Log `/tmp/dead-build-auth-cache.log`.
- Final local baseline build using `--jobs=8 --local_resources=cpu=2 --remote_executor= --bes_backend= --remote_cache=grpcs://remote.buildbuddy.io --remote_upload_local_results=false`: passed (exit 0), 484.088 seconds. Log `/tmp/dead-build-final.log`. No remote execution or cache uploads were used in these local attempts. About 2.3 GB of disposable cached download archives were removed after analysis to make room for build outputs; extracted repositories were retained.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3