# Docker → OCI migration result

Completed `tools/probers` in commit `08fd8760a` (`Remove legacy Docker image and push rules for probers`). Skipped the other five packages because their OCI bases differ from the legacy bases. There is one commit for the one changed package; no empty commits for skipped packages. This report is intentionally uncommitted.

## Changes and behavior

- Removed `//tools/probers:probers_image`, `//tools/probers:push_probers_image`, and their rules_docker load.
- Kept `probers_oci_image` and `push_probers_oci_image`, including their existing OCI repository and tagging configuration.
- Added `pkg_files(name = "image_files", attributes = pkg_attributes(mode = "0555"))` before `files_tar`. The built OCI image initially packaged its three Go binaries as `0755`; the legacy image used `0555`. Go executable metadata overrides `pkg_tar` mode settings, so the explicit mapping is necessary to preserve permissions.
- Only `tools/probers/BUILD` changed in the commit. No shared dependency files, callers, or `enterprise/tools/rbeperf/BUILD` changed. The only additional source file is this requested report.

The corrected probers image comparison passed on a remote runner: identical image `config` (entrypoint, environment, user, working directory), identical 33 base layer diff IDs, and identical 11 payload entries by normalized path, SHA-256 file contents, file type, permissions, UID/GID, and symlink target. The `/bazel` directory also has identical mode `0755` and ownership `0:0`. Docker has 34 layers; OCI has 35 because the existing OCI definition separates the Bazel binaries tar and other files. Layer archives and image digests are therefore not byte-identical. Entrypoint remains `/cloudprober --logtostderr`; all three prober binaries, cloudprober, busybox, and the Bazel binaries are retained.

Comparison evidence: [successful remote build and comparison](https://app.buildbuddy.io/invocation/bf5a3eb1-2432-4ee7-b65f-64f47636fa38). The comparison uses Python `json`, `tarfile`, and `hashlib` to compare the Docker `.0.config` against the OCI config blob, the base diff IDs, and the merged payload members. It checks runtime payload equivalence rather than tar timestamps/history metadata.

## Skipped packages

`tools/tcpproxy`, `tools/cacheload`, `tools/smarter_device_manager`, `enterprise/tools/mdload`, and `codesearch/cmd/server` all use:

| Definition | Base | Digest |
| --- | --- | --- |
| Legacy Docker (`deps/docker.MODULE.bazel`) | `gcr.io/distroless/cc-debian13` | `sha256:09013806abdc61e1e88f9bb018a7590245e4c520c55161df494fa8088f5655ea` |
| OCI (`deps/oci.MODULE.bazel`) | `gcr.io/distroless/cc-debian12` | `sha256:388145607c79313a1e49b783a7ee71e4ef3df31d87c45adb46bfb9b257b643d1` |

Their binary inputs correspond, but the OS/runtime base is not equivalent. Fixing the shared OCI pull would require editing a prohibited shared dependency file and would affect other images. These packages remain unchanged under the task's explicit skip option. The original estimated removal of 12 rules is therefore not supported by the current definitions; the actual removal is two Docker rules, with one packaging rule added. The probers Docker and OCI bases both use `gcr.io/distroless/java17-debian12` at digest `sha256:8bb82ccf73085b71159ce05d2cc6030cbaa927b403c04774f0b22f37ab4fd78a`.

## Callers and direct reverse dependencies

Searched the repository with `rg --hidden --no-ignore`, excluding `.git` and generated Bazel outputs, for all six packages and their old labels. This included `.github`, deployment, docs, scripts, and website files. There is no `buildkite` or `.buildkite` directory in this checkout. No references to either removed probers label existed outside its BUILD file, so no aliases or caller edits were needed.

Ran remotely:

```sh
bb remote query 'rdeps(//..., set(//tools/probers:probers_image //tools/probers:push_probers_image //tools/probers:probers_oci_image //tools/probers:push_probers_oci_image), 1)' --output=label_kind
```

This failed with exit 7 because unrelated Dockerfile repositories require a Docker daemon unavailable on the runner. Retrying with `--keep_going` returned partial results (exit 3): the four roots and `//tools/probers:probers_oci_image_index_json`, with no other callers. [Partial query evidence](https://app.buildbuddy.io/invocation/86cbd348-4709-46bb-a20a-3804015dc666). The full reverse-dependency result remains unverified due to those unrelated repository fetch failures; the repository-wide text search found no removed-label callers.

## Before/after measurements

Used the same query expression and output format before and after:

```sh
bazel query 'set(//tools/tcpproxy:* //tools/cacheload:* //tools/smarter_device_manager:* //enterprise/tools/mdload:* //codesearch/cmd/server:* //tools/probers:*)' --output=label_kind
```

The baseline ran locally before any edits; the final command ran via `bb remote query` after the user prohibited further local Bazel work. Both succeeded. [Final query](https://app.buildbuddy.io/invocation/098f1e54-84e2-40c9-ad8c-10f4aefd0e22).

| Metric, across the six packages | Before | After |
| --- | ---: | ---: |
| Docker image/push rules (`_app_layer`, `container_image_`, `container_push_` in query output) | 12 | 10 |
| Expanded Bazel rules (query lines containing ` rule `) | 63 | 62 |
| All queried labels, including generated/source files | 139 | 131 |
| BUILD LOC | 430 | 405 |

LOC used the identical command at both revisions:

```sh
wc -l tools/tcpproxy/BUILD tools/cacheload/BUILD tools/smarter_device_manager/BUILD enterprise/tools/mdload/BUILD codesearch/cmd/server/BUILD tools/probers/BUILD
```

Probers alone changed from 70 to 45 lines. All five other BUILD files are unchanged. No binary-size or dependency-closure reduction is claimed.

## Build/check results

- Baseline local background build: `bazel build --jobs=2 --bes_backend= //tools/probers:probers_image //tools/probers:probers_oci_image` — passed, 3203 seconds. Its initial config/layer comparison detected the binary mode mismatch described above.
- The subsequent local `rdeps` query filled the VM disk. The user deleted `~/.cache/bazel` and prohibited further local Bazel commands. All subsequent Bazel work ran through `bash -lc 'cd ~/repo && bb remote ...'`.
- Remote baseline with the permission fix: `bb remote build --jobs=2 //tools/probers:probers_image //tools/probers:probers_oci_image` — passed. [Build](https://app.buildbuddy.io/invocation/054ecb00-608b-4109-88b3-89ba4ea48107).
- Remote Python comparison — passed after correcting one verification-script incompatibility with the runner's older Python (`str.removeprefix`). The preceding script attempt built successfully but failed in the comparison script; it did not reveal an image mismatch.
- Final retained targets and direct helper: `bb remote build //tools/probers:probers_oci_image //tools/probers:push_probers_oci_image //tools/probers:probers_oci_image_index_json` — remote Bazel build passed. [Build](https://app.buildbuddy.io/invocation/853a22e3-61d7-4540-ad6c-9afe32fb5559). The local `bb` client subsequently exited 1 downloading a 2977-byte output blob (`NotFound`, exhausted peers). This is an artifact-download failure after the successful build; the earlier remote image comparison completed entirely on the runner and passed.
- Other five OCI images: `bb remote build //tools/tcpproxy:oci_image //tools/cacheload:oci_image //tools/smarter_device_manager:oci_image //enterprise/tools/mdload:oci_image //codesearch/cmd/server:oci_image` — passed. [Build](https://app.buildbuddy.io/invocation/ba3ba1f3-fb18-4bd5-91dc-6d60e5ff76ef). Passing builds do not resolve their base-image mismatch.
- `git diff --check` — passed. No application code changed, so no new application tests were added; built-image comparison covers the packaging behavior risk.

No image was pushed, no PR opened, and no commit pushed. Origin's fetch URL was temporarily changed for remote builds and restored to `https://github.int.exe.xyz/dan-stowell/buildbuddy.git`; its push URL is the same internal URL.

## External modules

No external module becomes unused. The binary/file dependencies remain in the OCI payload, and rules_docker still has numerous first-party users. Evidence: `rg -n '@io_bazel_rules_docker' --glob '*BUILD*'` returned 37 matching lines before and 36 after; remaining users include `server/cmd/buildbuddy/BUILD`, `enterprise/server/cmd/cache/BUILD`, and the untouched `enterprise/tools/rbeperf/BUILD`. The five skipped packages still use the Go Docker base, and probers retains its existing OCI Java base. Shared dependency files were left untouched.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3