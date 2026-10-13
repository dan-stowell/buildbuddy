# Deleted unused proto schemas

Removed buck_data.proto, buck_error.proto, buck_host_sharing.proto, the local bytestream.proto, and kythe_service.proto, plus their five proto_library and five Go rules in proto/BUILD. All five full reverse-dependency queries passed remotely before editing. Their results contain only the candidate subtree's proto and Go rules. No real consumers were found in the repository source audit.

## Before / after

Ran the same query before and after through BuildBuddy Remote Bazel:

```sh
bash -lc "cd ~/repo && bb remote <runner flags> query 'kind(\".*proto.*\", //proto/...)'"
```

This runs the underlying `bazel query 'kind(".*proto.*", //proto/...)'` remotely. Because bb streams runner logs alongside query stdout, the label lines were extracted into target lists and counted with `wc -l`; both underlying queries exited 0. This yields the same count as the requested Bazel query stdout piped directly to wc.

| Metric | Before | After | Reduction |
| --- | ---: | ---: | ---: |
| Proto-related targets, query above | 289 | 279 | 10 |
| Candidate schema files | 5 | 0 | 5 |
| proto_library declarations in proto/BUILD | 101 | 96 | 5 |
| go_proto_library declarations in proto/BUILD | 101 | 96 | 5 |
| ts_proto_library declarations in proto/BUILD | 78 | 78 | 0 |
| LOC across proto/BUILD and the five candidate files (`wc -l`) | 6,666 | 2,559 | 4,107 |

Rule counts use the same anchored declaration pattern for each kind before and after. The schema files contained 4,017 lines; their removed BUILD rules account for another 90 lines. Buck contained 314 messages including nested messages. This checkout had no TS rules for these schemas, so the survey's estimated four TS deletions do not apply.

Count invocations: [before](https://app.buildbuddy.io/invocation/94ecc50f-c4e8-4737-9b79-b8d734ce4cb2), [after](https://app.buildbuddy.io/invocation/c2e97b56-1c30-49a7-bb5e-e33bcaa18dee).

## Consumer verification

Each command ran in a login shell using bb remote, with these runner flags:

```sh
--runner_exec_properties=init-dockerd=true
--runner_exec_properties=EstimatedFreeDiskBytes=64GB
--runner_exec_properties=EstimatedCPU=8
```

The verification commands were:

```sh
bb remote <runner flags> query --experimental_scale_timeouts=4 \
  'rdeps(//..., //proto:<schema>_proto)'
```

| Schema | Complete rdeps result, all in //proto | Result |
| --- | --- | --- |
| buck_data | `buck_data_go_proto`, `buck_data_proto` | [passed](https://app.buildbuddy.io/invocation/3039f658-92cf-4350-992f-69379fb73777) |
| buck_error | `buck_data_go_proto`, `buck_data_proto`, `buck_error_go_proto`, `buck_error_proto` | [passed](https://app.buildbuddy.io/invocation/bf34115a-22f2-458c-9603-f7ae701318a4) |
| buck_host_sharing | `buck_data_go_proto`, `buck_data_proto`, `buck_host_sharing_go_proto`, `buck_host_sharing_proto` | [passed](https://app.buildbuddy.io/invocation/2e007e14-cc4f-4116-ace5-95ee9c0ee35f) |
| bytestream | `bytestream_go_proto`, `bytestream_proto` | [passed](https://app.buildbuddy.io/invocation/64bc94c6-05ba-49ff-b1b3-6ce4419774bd) |
| kythe_service | `kythe_service_go_proto`, `kythe_service_proto` | [passed](https://app.buildbuddy.io/invocation/2244469d-c7d1-4294-bf0d-59dda7d840fd) |

Also ran the original task's direct-rdeps check for the set of all ten removed proto/Go targets:

```sh
bb remote <runner flags> query --experimental_scale_timeouts=4 \
  'rdeps(//..., set(//proto:buck_data_proto //proto:buck_error_proto //proto:buck_host_sharing_proto //proto:bytestream_proto //proto:kythe_service_proto //proto:buck_data_go_proto //proto:buck_error_go_proto //proto:buck_host_sharing_go_proto //proto:bytestream_go_proto //proto:kythe_service_go_proto), 1)'
```

[Passed](https://app.buildbuddy.io/invocation/dbedf17e-d14b-47d2-a6d5-b444a1a4ef54), returning exactly those ten targets. There were no surviving direct rdeps to add to the build scope.

Before editing, refreshed the repository-wide grep audit. Extracted every message, enum, service, package, and declared Go package name from each candidate schema; searched all tracked repository files with `rg -n -w -F -f <patterns>`, including app/, enterprise/app/, website/, cli/, server/, enterprise/server/, protos, BUILD files, patches, and documentation. Also searched the generated import paths and target names. Prior hidden/no-ignore searches covered non-generated workspace files as well.

Findings:

- Buck package/import references occurred only in the candidate schemas and proto/BUILD. BuckEvent's two outside-schema mentions were a TODO about future support and a blog discussion. Other symbol matches, such as Error, Snapshot, and TestResult, referred to unrelated definitions or prose.
- Local bytestream generated imports had no consumers. Existing ByteStream code imports `google.golang.org/genproto/googleapis/bytestream`; remote_execution_log uses `@googleapis//google/bytestream:bytestream_proto`. Kythe ReadRequest/WriteRequest references are qualified `kythe.proto`. These live external uses are preserved.
- kythe.service and the local kythe_service generated import had no consumers. Outside-schema GraphStore matches were comments in buildpatches/io_kythe.patch.
- buck_host_sharing's Shared matches outside the candidate subtree were unrelated fields/comments. buck_error's package references were confined to the dead Buck subtree.

After deletion, the package/import/local-target search returned no remaining references to the removed libraries.

## Build / checks

Ran remotely with the local deletions included:

```sh
bash -lc "cd ~/repo && bb remote <runner flags> build \
  --experimental_scale_timeouts=4 //server //enterprise/server \
  //enterprise/server/cmd/executor:executor //app:app"
```

[Passed, exit 0](https://app.buildbuddy.io/invocation/0949c8b6-2d35-460c-a79d-69a5fef2c518). All four requested targets built successfully in about 74 seconds. The runner log confirms all six file changes were applied before the build. Bazel reported 20,191 actions, including 18,683 remote cache hits and 11 linux-sandbox actions.

`git diff --check` also passed. No tests were added or run: this change removes verified dead schemas and generated rules; the full and direct rdeps checks found no live behavior or affected test targets.

Earlier local verification was blocked by Docker repository-rule timeouts and disk exhaustion. Initial remote attempts encountered an unstarted Docker daemon, and an initial baseline remote build exhausted its default disk allocation. The documented runner properties above addressed these issues; successful query and build results above supersede the failed setup attempts. No dependency files were changed to work around them.

## External dependencies, scope, and omissions

No external module became unused. The removed rules used the shared protobuf toolchain/runtime and Kythe; surviving proto/BUILD rules still depend on protobuf and `@io_kythe` (search proto/Go rules and Kythe TS wrappers). Google ByteStream remains referenced in proto/BUILD and server/remote_cache/byte_stream_server/BUILD, and imported by many first-party Go packages. Evidence is available with:

```sh
rg -n '@io_kythe|@org_golang_google_genproto_googleapis_bytestream|@googleapis//google/bytestream' \
  proto/BUILD server/remote_cache/byte_stream_server/BUILD
rg -n 'google.golang.org/genproto/googleapis/bytestream' server enterprise cli codesearch
```

No shared dependency files or lockfiles were edited. The committed source scope is exactly proto/BUILD and the five deleted schemas. result.md is kept locally, untracked, as requested. The origin fetch/push URLs were updated as requested; nothing was pushed and no PR was opened.

VM-local query lists, grep patterns/hits, and logs are under `/tmp/dead-proto-*`. Downloaded remote build outputs are under bb-out/. No TS deletion or external-module cleanup was needed, and no binary-size reduction is claimed for libraries that were already unconsumed.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3