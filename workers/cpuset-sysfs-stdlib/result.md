Replace cAdvisor topology discovery in cpuset with direct sysfs reads

Linux cpuset now reads NUMA CPU membership from node*/cpulist, retaining the physical-package fallback when NUMA directories are absent. Both paths check cpu/online before accepting a CPU; an absent online file means every CPU is online, matching cAdvisor. Replace x/exp slices and constraints.Integer with standard-library slices and a local integer constraint. BUILD removes the corresponding four external dependency entries.

The review regression is covered explicitly: with CPU directories 0 and 1 and online=0, only CPU 0 is returned; with no online file, both are returned. Fixtures also cover ranges, commas, multiple and memory-only nodes, absent node directories, missing core/package IDs, malformed online lists, and malformed topology IDs.

Behavior audit against github.com/google/cadvisor v0.56.2:
- getCoresInfo checks online status before core/package topology. Preserve missing topology => skip, other read/parse errors => fail. Preserve s390x book/drawer file checks.
- Fallback retains the preliminary physical_package_id read even for offline CPUs, including its missing-file and malformed-ID behavior. Topology is checked again before accepting a CPU to handle disappearing hotplug files.
- Read online status per CPU, rather than caching a snapshot. Preserve cAdvisor's uint16 online-ID parsing, first-match behavior, and unreadable/malformed online lists => offline.
- NUMA membership comes from cpulist instead of node CPU symlinks, as requested. Discovery remains non-atomic under hotplug, as before. CPU ordering can change: cAdvisor groups threads into cores and fallback packages using map iteration; its API did not promise an order.
- Omit unused cache/memory/hugepage/distance discovery. Errors in that unrelated metadata no longer prevent CPU discovery. No consumers use those values.

Dependency evidence:
rg -n 'google/cadvisor|com_github_google_cadvisor' --glob '*.go' --glob BUILD --glob BUILD.bazel
Previously matched only the two cpuset imports and two BUILD labels; now returns no matches. No first-party cAdvisor users remain. Shared module declarations are deliberately untouched for the separate serialized dependency-removal PR. x/exp remains used elsewhere.

Before/after metrics, computed with the same /tmp/cpuset-metrics.py script against baseline 770b88bc8 and the final package:
- BUILD targets: 2 -> 2.
- External dependency entries (all platforms, excluding select keys): 8 -> 4.
- Unique external dependency labels: 7 -> 3.
- cAdvisor labels: 2 -> 0.
- External Go modules imported by non-test sources: 3 -> 1.
- Go LOC including tests: 819 -> 1050.
No binary-size claim was measured.

Validation:

- Final remote test: bash -lc "cd ~/repo && bb remote test //enterprise/server/util/cpuset:cpuset_test --test_output=errors" — PASS, exit 0, including nogo. Invocation: https://app.buildbuddy.io/invocation/88644a2b-3809-4610-8242-5d0106faddfb
- Initial remote attempts exposed and resolved maps.Copy / strings.SplitSeq lint requirements and newline handling in the fixture generator. No production test failure remains.
- Direct users were identified previously by Bazel deps/embed attribute queries over //enterprise/...; whole-repository rdeps query failed while fetching an execution-image repository. Global BUILD grep confirmed all explicit cpuset references are in enterprise. No broad build was attempted.
- Earlier local validation (before the review fix) passed cpuset tests and built 7/8 affected/direct-user targets; Firecracker image conversion failed because it requires --config=remote. The review fix is validated remotely, without additional local Bazel builds.
- Successful earlier direct-user query: bazel query --bes_backend= 'attr("deps", "//enterprise/server/util/cpuset:cpuset", //enterprise/...) union attr("embed", "//enterprise/server/util/cpuset:cpuset", //enterprise/...)'.
- Remote direct-user builds without --config=remote failed at the unrelated Firecracker image genrule ("image conversion requires root; build with --config=remote"). Retried with:
  bash -lc "cd ~/repo && bb remote build --config=remote //enterprise/server/cmd/executor:executor_lib //enterprise/server/remote_execution/cgroup:cgroup //enterprise/server/remote_execution/containers/firecracker:firecracker //enterprise/server/remote_execution/containers/firecracker:firecracker_test //enterprise/server/remote_execution/containers/ociruntime:ociruntime_test //enterprise/server/util/cpuset:cpuset_test //enterprise/tools/vmstart:vmstart_lib //enterprise/server/util/cpuset:cpuset"

Branch preparation:
Removed the previously committed enterprise/server/util/cpuset/result.md with git rm --cached and amended the single branch commit, retaining both reports on disk. git log --format= --name-only origin/master..HEAD contains no result.md. git diff --name-only origin/master...HEAD lists only enterprise/server/util/cpuset/{BUILD,cpuset.go,numcpu_linux.go,numcpu_linux_test.go}. Root result.md is untracked (locally ignored), never committed. No push or PR.
- Final direct-user remote build with --config=remote: PASS, all 8 targets, exit 0, including Firecracker images and nogo. Invocation: https://app.buildbuddy.io/invocation/d8c3b930-4f00-4cb0-8e6b-8804c42a6903 . Unrelated NVIDIA deprecation warnings remain.
- git diff --check origin/master...HEAD: PASS.
- Restored origin fetch and push URLs to https://github.int.exe.xyz/dan-stowell/buildbuddy.git .
- Final commit: 6b52f706e (Read cpuset topology from sysfs and use stdlib slices).


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3