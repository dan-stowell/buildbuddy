# Non-Go dependency and proto survey

Research only, at commit `770b88bc89c0b20059d42a237e8c7923ff5b7215` on 2026-10-12 UTC. Bazel 9.3.0rc2; Linux x86_64; 25 GB VM disk. No PR, push, commit, or retained dependency/code change. A temporary esbuild metafile edit was reverted. Supporting measurements and complete inventories are in [survey-data](survey-data/README.md).

## 1. Summary: five biggest wins

| Rank | Win | Measured scope and estimated impact |
|---|---|---|
| 1 | Replace the query graph's bundled `dagre-d3-react` renderer with a small renderer using native SVG and a focused layout implementation. | **One caller**; **833,116 bytes** attributed to this package in the default app bundle, **6.7% of its unminified JS**. Removing it and its dedicated `@types/dagre-d3` chain eliminates **4 exclusive npm snapshots**, **6,428,329 unpacked bytes**. Replacement cost must be subtracted; no minified saving is asserted. |
| 2 | Replace the website's decorative globe with a lightweight visual. | **One importing component**, used on the remote-cache page; **45 exclusive snapshots** out of 1,315 website snapshots. At least **144,156,716 unpacked bytes** disappear if the entire globe stack is removed. Two package sizes were unavailable. This improves website installs/builds, not Bazel server builds. |
| 3 | Delete dead proto schemas and language outputs together. | **9 generated libraries** have no non-proto consumers: **5 Go**, **4 TS**. The dead Buck subtree alone defines **314 messages**. Delete **5 schema files**, **5 proto-library rules**, **5 Go-library rules**, and **4 TS-library macro calls**; the latter also remove **4 plugin action targets**. These are already unlinked from application binaries, so their direct binary-size saving is **0**; benefit is wildcard build/codegen/analysis work. |
| 4 | Complete the existing Docker-to-OCI convergence in independent small packages. | **7 packages already define both old image/push rules and OCI equivalents**: **14 redundant Docker-specific image/push rules** across those packages. The full Docker stack spans **20 BUILD files**, while k8s has **one handwritten object and one aggregation caller**. These seven migrations are useful steps, but do **not** individually remove `rules_docker` or its Java/Python graph. Image base versions and destination names differ, so they require parity checks. |
| 5 | Simplify Python BUILD generation and remove duplicate generated libraries. | **4 py_library targets** have only themselves as reverse dependencies, duplicating source lists already built by py_binary rules. Python Gazelle machinery is loaded only from root BUILD and expands to **5 dedicated mapping/manifest targets** plus a language entry in the root Gazelle binary. Hand-maintaining this small Python surface can remove **1 direct rule module**, those **5 targets**, and the **4 duplicate libraries**. Keep the Python runtime, pip requirements and uv updater: they have real callers. |

Fast, smaller wins: remove the unsharded tests' extra Jasmine core (**403,185 unpacked bytes**, one package); use bundled `diff` types (**16,268 bytes**, one package); replace the single UUID call with the browser API, eliminating UUID and its old types (**76,936 bytes**, two packages); remove **5 unused dockerfile repository declarations**. None of those is advertised as a major server binary saving.

## 2. Measurements and commands

### Baselines and limits

| Measurement | Output | Interpretation |
|---|---:|---|
| `bazel mod graph` | **71 selected dependency modules**, **37 directly declared** | Includes Go *rule sets*, not Go module dependencies. Module selection is not a download count. |
| Root repository mapping, excluding Go module extension entries and main repo | **149 apparent external aliases**, **149 canonical external repos** | `WORKSPACE` contains only a compatibility comment. Includes 67 static repos, containers, rule modules and imported extension hubs. |
| Complete first-party `bazel query //... --output=xml` | **5,043 expanded rules**, **866 loaded packages** | Target-input graph used for all direct caller and reverse-dependency calculations. |
| Failed full attempt's `external/` directory snapshot | **754 external directory entries**, **372 outside the Go module extension** | Observed materializations, including built-ins/configuration repos and possibly incomplete fetches. Cache-backed entries count. **Not a successful full-build fetch total.** |
| Root npm manifest / project lock document | **48 direct deps**, **148 package records**, **148 snapshots** | Also **10 helper package records** in a separate lockfile YAML document: **158 total records**. |
| Website manifest / project lock document | **17 direct deps**, **1,312 package records**, **1,315 snapshots** | Also **10 helper records**: **1,322 total records**. Peer-context snapshots are not distinct package versions. |
| Both npm projects | **65 direct declarations**, **1,480 package records across both locks/documents** | Sum, not a globally deduplicated install. Website does not participate in Bazel `//...` npm translation. |
| All tracked proto files | **117** | Includes test protos. Expanded first-party graph contains **107 proto_library**, **107 go_proto_library**, **79 TS plugin rules**. |
| Scoped app transitive query | **2,620 labels**, **131 canonical external repos**, **120 non-Go repos**, including **98 npm package repos** | Successful query; includes non-runtime/tool dependencies. |
| Scoped OSS server + executor binary query | **17,492 labels**, **504 canonical external repos**, **138 non-Go repos** | Successful query. Server embeds the app, accounting for its npm/proto closure. Excludes Go module repos only after alias normalization. |

Exact **successful full-build fetch count is blocked**, rather than estimated from module or lockfile totals. The attached [repository inventory](survey-data/repository-inventory.tsv) lists **710** declared/locked/observed non-Go canonical repos, including npm link repos, all observed toolchain variants and repos materialized by later scoped checks. It records every direct first-party input caller and successful scoped-query presence. It is not evidence that all 710 download archives, or that an unobserved transitive extension has no other possible platform repos.

Full attempts and narrowed work:

```sh
bazel query 'deps(//...)' --output=label
# Loading: 3265 packages loaded
# ERROR: extracting ...org_chromium_chromium_linux_x64/...chrome-linux.zip:
# No space left on device
# Evaluation of query "deps(//...)" failed: preloading transitive closure failed

bazel build //... --nobuild --bes_backend= \
  --build_event_json_file=/tmp/deps-survey/full-analysis.bep
# ERROR: downloading clickhouse-common-static-25.3.14.14-amd64.tgz:
# No space left on device
# Analysis of target '//tools/clickhouse_bin:clickhouse_bin' failed
# INFO: 0 processes. Build did NOT complete successfully

bazel build //server/... //enterprise/server/... --jobs=2 --bes_backend=
# Analyzing: 870 targets; ExtractBazelInstall ...bazel-9.2.0_outdir
# Interrupted deliberately at 203 MB free to prevent another disk exhaustion.
# Elapsed 27.234s; 37 processes; build interrupted.

bazel build //app:app_bundle --jobs=2 --bes_backend=
# First attempt: Node archive extraction failed with No space left on device.
# After removing survey-downloaded cross-platform browser/image caches and
# partial Bazel-install outputs/sandboxes, retry succeeded:
# Analyzed: 423 packages, 119250 configured targets, 229 aspect applications
# Elapsed time: 169.537s; Critical Path: 64.94s
# 1565 processes: 351 internal, 1062 linux-sandbox, 152 local
# Build completed successfully, 1565 total actions
```

The full `//...` attempt stopped in analysis (`--nobuild`), so no full compilation was completed or claimed. The app timing is a first app build in this session after dependency queries, **not a pristine clean-cache benchmark**. Source checkout stayed intact; cache cleanup affected survey-generated/downloaded data only. Logs are linked in the [evidence guide](survey-data/README.md).

Successful scope commands:

```sh
bazel --output_base=/tmp/deps-survey/bazel-firstparty query //... \
  --output=xml --bes_backend=
bazel --output_base=/tmp/deps-survey/bazel-firstparty \
  query 'deps(//app:app_bundle)' --bes_backend=
bazel --output_base=/tmp/deps-survey/bazel-firstparty \
  query 'deps(set(//server/cmd/buildbuddy:buildbuddy //enterprise/server/cmd/executor:executor))' \
  --bes_backend=
bazel --output_base=/tmp/deps-survey/bazel-mod mod graph --bes_backend=
bazel --output_base=/tmp/deps-survey/bazel-mod mod dump_repo_mapping '' --bes_backend=
```

### Rule sets, external repos and toolchains

The [module inventory](survey-data/module-inventory.md) enumerates **all 71 selected modules**, selected versions, all 37 direct declarations, scoped reachability, and first-party files referencing directly declared modules. The [repository inventory](survey-data/repository-inventory.tsv) enumerates the external repositories with their exact direct first-party target callers. These inventories distinguish rule-file loads from target dependency edges: absence from `deps()` alone cannot justify deleting a toolchain or loaded rule set.

Useful small-caller groups, measured from the complete XML and tracked source:

| Dependency family | First-party callers / measured use | Finding |
|---|---|---|
| `rules_k8s` | Only `deployment/BUILD`; handwritten `buildbuddy_dev_app` and `dev`; **14 expanded targets** reference the k8s repo | One dev deployment keeps a forked module and kubectl extension around. Replace this small rendering/apply path before removing module and patch. |
| `rules_python` | **7 BUILD files**, **8 py_binary** and **5 py_library** expanded rules | Not a single-script toolchain. One binary is manifest update. Requests has three source importers; packaging has one. |
| `rules_python_gazelle_plugin` | Only root BUILD: `bb_gazelle_binary`, generated main, `modules_map`, manifest generation/test/update | Good simplification candidate; preserve real Python binaries. |
| `rules_uv` | Only root `requirements`/`requirements_test` updater | Retain: an intentional maintenance command, not dead because production never depends on it. |
| `rules_bazel_integration_test` | Only `tools/dev_qa_test/BUILD.bazel`, **4 integration test calls** | Narrow caller surface, but deliberate compatibility coverage; retain. Bazel 8.4.2 and Bazelisk extension imports support these tests. |
| `rules_webtesting` | **7 web_test rules**, plus `server/testutil/webtester`; browser extension exposes **8 platform repos** | Real test coverage. Portable unconfigured query traversed other-platform browser downloads. Mac arm64/mac x64/Windows browser dirs alone were **274/293/437 MiB** by `du -h`; do not interpret that as Linux configured-build download cost. |
| `rules_docker` / `rules_oci` / `rules_img` | **20 / 10 / 3 BUILD files** loading these families | Overlapping image construction. `rules_img` callers are two distroless packages and enterprise server packaging; they are not abandoned. |
| `redis` | `enterprise/server/testutil/testredis/BUILD`, `enterprise/tools/shardedredis/BUILD` | Two legitimate callers, including test infrastructure. |
| `bazel_worker_api` | `proto/BUILD` imports worker proto | Keep: meaningful proto input. |
| TypeScript | `npm_typescript` uses **7.0.2**; `npm_typescript6` uses **6.0.2** as validator for the shared ts_library macro | Deliberate workaround: TS7 lacks the programmatic validator API. Not an unused second compiler. |
| C/C++ | LLVM **0.8.23** registered toolchains, platform definitions, lint tool, Go/proto tool setup; MSVC registered for Windows | Do not infer non-use from no handwritten cc_library. C++ runtime compilation occurred in the successful app build. |
| Java / Kotlin / Android / Rust | **0 handwritten Java/Kotlin/Android/Rust build rules** in first party; selected transitively | These are inherited module-graph cost, not evidence of Java sources shipping in the server. Java also comes through Docker, distroless, stardoc and with_cfg. |

Command tracing inherited modules:

```sh
bazel --output_base=/tmp/deps-survey/bazel-mod mod explain \
  rules_k8s rules_docker rules_java rules_android rules_kotlin rules_rust \
  rules_python with_cfg.bzl --bes_backend=
# Selected paths (trimmed from mod-explain.txt):
# root -> aspect_bazel_lib -> stardoc -> rules_java@9.3.0
# root -> llvm -> with_cfg.bzl -> rules_java@9.3.0
# root -> protobuf@35.1 -> rules_jvm_external@6.9 -> rules_android@0.7.1
# root -> protobuf@35.1 -> rules_kotlin@2.3.20
# root -> protobuf@35.1 -> rules_rust@0.69.0
# root -> rules_k8s -> rules_docker -> rules_java / rules_python
# root -> rules_distroless -> rules_java
```

**67 static repositories** are declared in `deps.bzl`, including **28 Bazel version/platform binaries**, **5 Bazelisk binaries**, **4 ClickHouse variants**, **4 prebuilt bb CLI variants**, **3 VM kernels**, and platform variants of Firecracker, Podman, containerd, crun, runc, tini, rootlesskit, credential-gcr and BusyBox. The inventory records macro-expanded consumers, avoiding false positives from concatenated labels:

```sh
python3 survey-data/scripts/static.py
python3 survey-data/scripts/detail.py
# STATIC REPOS 67
# server/util/bazel/BUILD: versions = 6.5.0,7.7.1,8.3.1,8.4.2,8.5.0,9.0.0rc3
# server/testutil/testbazel/BUILD: versions = 9.2.0
# tools/clickhouse_bin/BUILD creates the Darwin labels using arch concatenation
# tools/probers references @cloudprober via shorthand label
```

**Five registered Dockerfile repos have zero direct first-party target-input references** in the complete graph: `rbe-ubuntu20-04_image`, `rbe-ubuntu20-04-webtest_image`, `rbe-ubuntu20-04-workflows_image`, `rbe-ubuntu22-04_image`, `rbe-ubuntu24-04_image`. Their old push definitions in enterprise deployment are commented out; image maintenance now documents `enterprise/tools/build_images/build_image.py`. Keep the Dockerfiles and deployed image URIs: references to a remote container URI do not consume a Bazel Dockerfile repo. These five repos were **not materialized** in the failed full attempt; deletion simplifies declarations, with **zero demonstrated fetch saving**.

### npm, duplication and package weight

Commands:

```sh
python3 survey-data/scripts/npm-analysis.py
# root: package records [10,148], snapshots [10,148]
# website: package records [10,1312], snapshots [10,1315]
# SIZE root dagre-d3-react: 1 exclusive record, 6277600 bytes, 0 unknown
# SIZE root @types/dagre-d3: 3 exclusive records, 150729 bytes, 0 unknown
# SIZE website react-globe.gl: 45 exclusive records, 144156716 bytes, 2 unknown
# SIZE root jasmine-core: 1 exclusive record, 403185 bytes, 0 unknown
# SIZE root libsodium-wrappers: 2 exclusive records, 760747 bytes, 0 unknown
# SIZE root moment: 1 exclusive record, 4350323 bytes, 0 unknown
# SIZE root monaco-editor: 1 exclusive record, 92234035 bytes, 0 unknown
```

These sizes come from npm registry `/<package>/<locked-version>` distribution metadata; exact responses are retained in `npm-metadata.json`. The closure algorithm and peer/optional handling are in the script. Remove a package **and regenerate its lock** to realize an exclusive-closure reduction; downloaded optional platforms need separate accounting.

Measured direct callers from the XML, excluding npm aggregation/link rules:

| Package | Direct first-party callers | Interpretation |
|---|---:|---|
| dagre-d3-react | **1**: `//app/invocation:invocation_query_graph_card` | Bundles a large implementation for a specialized card. |
| libsodium-wrappers | **1**: `//enterprise/app/secrets:secret_util` | Used for GitHub-compatible sealed-box encryption; retain, see rejected items. |
| uuid | **1**: `//app/shortcuts:shortcuts` | One `v4()` call creates shortcut handles. Browser `crypto.randomUUID()` can replace this, subject to supported browser/secure-context checks. |
| diff-match-patch | **1**: `//app/util:diff` | `diff` is separately used by **2** code editor targets. Consolidation needs behavior tests, not a blind dependency swap. |
| memoize-one | **2**: terminal and text targets | Root version **6.0.0**, transitively **5.2.1** through react-window; assess using the already-installed 5.x API. |
| react-resize-detector | **1**: trends heatmap | Useful isolated candidate for browser ResizeObserver, but no measured saving or prototype; not added to the ranked tasks. |
| monaco-editor | **9** | Too many actual callers to call unused; **92 MB install size** is not proof all languages enter the bundle. |
| date-fns / moment | **4 / 19** | Date handling overlaps; moment removal is broad rather than one small PR. |
| recharts / rxjs | **9 / 21** | Broad legitimate usage; do not remove for install weight alone. |

The root lock contains **9 package names with multiple versions**:

```text
@babel/runtime 7.26.10 / 7.27.0
@types/d3-scale 3.3.5 / 4.0.9
@types/d3-time 2.1.4 / 3.0.4
@types/node 12.20.55 / 25.8.0
jasmine-core 5.6.0 / 6.1.0
long 4.0.0 / 5.3.2
memoize-one 5.2.1 / 6.0.0
react-is 16.13.1 / 18.3.1
tslib 1.14.1 / 2.8.1
```

Website has **126 multiple-version package names**; most are within Docusaurus's broad graph. No orphan project package records without snapshots were found in either lock. Direct manifests alone are not an unused-package test: JSX types, generated code, BUILD macros, MDX and Docusaurus config consume packages without ordinary source imports.

Specific unused/redundant evidence:

- **Extra Jasmine core 5.6.0:** XML has **21 jasmine_test targets, all with shard_count unset**. `aspect_rules_jasmine/jasmine/defs.bzl` adds `node_modules/jasmine-core` only when shard_count is set; `private/runner.cjs` requires it only when `TEST_TOTAL_SHARDS` is nonzero. `jasmine@6.1.0` already depends on core **6.1.0**. Remove the direct old core only after running all 21 tests, including a forced-sharding check if that workflow must be supported. `jasmine-reporters` is **not unused**: the macro enables its JUnit reporter by default.
- **Old diff types:** registry metadata for `diff@8.0.3` says `types: libcjs/index.d.ts` and conditional type exports. `@types/diff@5.2.3` is a separate **16,268-byte** package referenced by two editor BUILD targets. Use the package's bundled types.
- **Old UUID types:** `uuid@14.0.0` exports `./dist/index.d.ts`; the separate UUID 8 types are **6,671 bytes**. The UUID replacement task removes both packages and the BUILD type label together.
- **D3 type duplication:** a direct d3-scale 3.x types pin coexists with 4.x types from recharts, while runtime d3-scale is 4.x. Moving the direct type pin to compatible 4.x should eliminate that old type record and its otherwise-exclusive d3-time 2.x record. Verify heatmap/common type checking; closure reduction is an estimate until regenerated.
- **Do not delete `@types/long`:** runtime long **4.0.0 has no bundled types**. The modern protobufjs transitive long 5.x does not automatically replace that explicit API/type dependency.

Bundle measurement, with `app/BUILD.bazel` temporarily setting `metafile = True` and then restored:

```sh
python3 survey-data/scripts/bundle-measure.py
cp bazel-bin/app/app_bundle_metadata.json /tmp/deps-survey/app-metafile.json
# App metafile prototype build: 3.188s, successful; edit restored in finally.
# Sum outputs[*].inputs[*].bytesInOutput by node_modules package:
# recharts          1,017,213
# dagre-d3-react       833,116
# lucide-react         715,667
# react-dom            250,634
# moment               155,684
# rxjs                  89,932
# date-fns              64,572
# diff-match-patch      58,174
# protobufjs            34,487
# long                  25,097
# Entire app.js: 12,410,446 bytes; gzip.compress: 1,429,513 bytes
# Source map: 7,755,258 bytes
```

A standalone `esbuild app.js --minify --outfile=/tmp/deps-survey/app.min.js` produced **5,933,279 bytes / 1,033,553 gzip bytes**. That is a transformation of the default output, **not a release Bazel build** or per-package minified attribution. Package attribution above is exact only for the default unminified output.

### Protos: dead schemas, unused language outputs, toolchain cost

Successful query and trimmed output:

```sh
bazel --output_base=/tmp/deps-survey/bazel-firstparty query \
 'rdeps(//proto/..., set(//proto:buck_data_go_proto //proto:buck_error_go_proto //proto:buck_host_sharing_go_proto //proto:bytestream_go_proto //proto:kythe_service_go_proto //proto:build_status_ts_proto //proto:execution_graph_ts_proto //proto:kythe_identifier_ts_proto //proto:server_notification_ts_proto))' \
 --bes_backend=
# Returns exactly those nine targets, no other targets.

bazel --output_base=/tmp/deps-survey/bazel-firstparty query \
 'rdeps(//proto/..., set(//proto:buck_data_proto //proto:bytestream_proto //proto:kythe_service_proto))' \
 --bes_backend=
# //proto:buck_data_proto             //proto:buck_data_go_proto
# //proto:bytestream_proto            //proto:bytestream_go_proto
# //proto:kythe_service_proto         //proto:kythe_service_go_proto

python3 survey-data/scripts/graph-analysis.py
# Complete first-party XML reverse traversal:
# buck_data_go_proto: 1 target (self)
# buck_error_go_proto: 2 (self + buck_data_go_proto)
# buck_host_sharing_go_proto: 2 (self + buck_data_go_proto)
# bytestream_go_proto: 1 (self)
# kythe_service_go_proto: 1 (self)
# build_status_ts_proto: 1 (self)
# execution_graph_ts_proto: 1 (self)
# kythe_identifier_ts_proto: 1 (self)
# server_notification_ts_proto: 1 (self)
```

The original full `rdeps(//..., set(...))` failed fetching Go SQLite after disk exhaustion; its error is retained. The successful scoped queries plus complete XML prove the **first-party** absence of consumers. Public visibility means external consumers are still a compatibility consideration.

- `buck_data.proto`, `buck_error.proto`, `buck_host_sharing.proto`: **308 + 0 + 6 message declarations**, no application consumer; Go outputs only form the dead subtree. Data schema is 98,449 bytes, host-sharing 1,102 bytes, error enums 12,109 bytes.
- `bytestream.proto`: local duplicate of the upstream Google ByteStream contract; production imports the upstream Go package and `remote_execution_log.proto` imports `google/bytestream/bytestream.proto`. Local Go output has no consumer.
- `kythe_service.proto`: local service definitions generate a Go library with no consumer. Keep the real Kythe protos used by code navigation.
- Only the **TS outputs** for build_status, execution_graph, server_notification and kythe_identifier are dead. Do **not** delete their schemas or used Go outputs, or the upstream identifier schema.
- No C++/Java/Python codegen rules are declared for these first-party schemas. Actual language plugins are Go message generation, Go gRPC where needed, vtprotobuf and optional wire views; TS protobufjs; specialized test generators. Go API-v1 and Jaeger have their own explicit compiler lists. The wrapper already distinguishes messages-only/services modes; don't generate extra gRPC code indiscriminately.
- Message-level deletion in live public/persisted schemas was **not established**. A field/message can be used through reflection, wire clients or generated references; raw name search does not justify deletion or field-number reuse.

Toolchain commands:

```sh
bazel aquery 'mnemonic("GoLink|ProtocGenPbjs|GenProto", deps(//app:app_bundle))' \
 --output=jsonproto --include_artifacts=false --include_commandline=false --bes_backend=
# 74 ProtocGenPbjs actions; 2 GoLink actions:
# //:vet_actual
# @@+install_static_dependencies_ext+com_github_buildbuddy_io_protoc_gen_protobufjs//:protoc-gen-protobufjs

bazel --output_base=/tmp/deps-survey/bazel-mod mod show_repo \
 @com_github_buildbuddy_io_protoc_gen_protobufjs @llvm @com_google_protobuf --bes_backend=
# protobufjs: v0.0.14 http_archive containing Go sources and go_binary
# llvm: 0.8.23; protobuf: 35.1

bazel aquery 'mnemonic("CppCompile", deps(//app:app_bundle))' \
 --output=jsonproto --include_artifacts=false --include_commandline=false --bes_backend=
# 1,488 CONFIGURED CppCompile actions, 23 targets:
# llvm compiler-rt, libcxx, libcxxabi, libunwind, glibc and helper tools.
# These are NOT 1,488 executed compile actions or a measured removal saving.

# Repeat the aquery with --@io_bazel_rules_go//go/config:pure=true:
# Still 1,488 configured CppCompile actions; 112,298 configured targets.
```

Protobuf **already registers prebuilt protoc toolchains** in `protobuf/MODULE.bazel`; rebuilding/replacing protoc is not a proven win. A prebuilt protobufjs plugin could avoid its Go linking, nogo and Go-stdlib setup for standalone frontend builds, but upstream prebuilt release artifacts would be needed. This is a follow-up investigation, not an independent ready task. The pure-Go flag experiment did not establish a C++ reduction.

## 3. Candidate tasks, ranked by gain per effort

Each row owns distinct non-shared files. Changes to dependency declarations/lockfiles are explicitly shared, **including `deps/*.MODULE.bazel` and the website manifest/lock**. Rows can be implemented independently; do not attach a global Docker removal to an individual image migration. No candidate has been implemented or build-tested after removal; verification instructions are the completion gate. “Package reduction” means project lock records/snapshots unless stated otherwise.

| slug | what to do | files/dirs it touches | touches shared dep files? | expected gain (numbers) | risk | how to verify |
|---|---|---|---|---|---|---|
| prune-dead-protos | Delete the Buck subtree, local unused ByteStream/Kythe service schemas and Go rules; delete the four unused TS outputs. Preserve used schemas/languages. | `proto/BUILD`; `proto/{buck_data,buck_error,buck_host_sharing,bytestream,kythe_service}.proto` | No | **5 files; 314 Buck messages; 14 handwritten rule/macro calls + 4 TS plugin targets**; no current linked-binary saving | Medium: public labels/contracts may have downstream users; Gazelle intentionally disabled for protos | Repeat scoped rdeps and whole first-party XML traversal; build used proto targets and server/executor binaries; check external published API expectations. |
| drop-extra-jasmine-core | Remove direct core 5.6.0; retain jasmine and its core 6.1.0, and retain reporters. | Root manifest/lock only | **Yes: package.json, pnpm-lock.yaml** | **1 npm record/version; 403,185 unpacked bytes** | Low/medium: ad hoc forced sharding may rely on the root link | Run all **21 Jasmine tests**; try `--test_sharding_strategy=forced=2` if supported in CI; if required, align the root core to 6.1.0 instead of removing the link. |
| retire-unused-rbe-repos | Delete five unused RBE Dockerfile repo declarations; retain Dockerfiles and current image build script. | `deps/docker.MODULE.bazel` | **Yes: included module file, MODULE.bazel.lock if changed** | **5 repo declarations**; **0 demonstrated download saving** because they weren't materialized | Low/medium: intentional external direct repo access | Regenerate mapping; query first-party XML; build affected deployment/image targets; confirm documented Python image commands still operate. |
| uuid-browser-api | Replace shortcut-handle `v4()` with `crypto.randomUUID()`; remove UUID and UUID-8 type labels/packages. | `app/shortcuts/shortcuts.tsx`, `app/shortcuts/BUILD`; root manifest/lock | **Yes: package.json, pnpm-lock.yaml** | **2 npm records**, **76,936 unpacked bytes**; one runtime dependency | Low/medium: supported browsers/secure contexts | Build app; exercise shortcut register/unregister uniqueness and supported localhost/HTTPS contexts. |
| diff-bundled-types | Remove old diff type package and its two editor BUILD deps; use diff 8's bundled declarations. | `enterprise/app/code/BUILD`; root manifest/lock | **Yes: package.json, pnpm-lock.yaml** | **1 record**, **16,268 unpacked bytes**; aligns type/runtime API | Low/medium: newer definitions may expose errors | Build/typecheck `//enterprise/app/code:code` and `:code_v2`; fix only declaration-compatibility errors in a follow-up if needed to preserve file ownership. |
| d3-types-align | Move the direct d3-scale type pin to compatible 4.x, letting both consumers share existing types; prune old d3-time 2.x transitively. | Root manifest/lock only | **Yes: package.json, pnpm-lock.yaml** | Estimated **2 old-version records** removed; retains runtime scale/time | Medium: type compatibility | Regenerate lock; prove old scale 3.3.5 and time 2.1.4 absent; typecheck trends heatmap/common. If source fixes are needed, reserve those files before parallel implementation. |
| simplify-python-generation | Remove Python Gazelle language/mapping/manifest setup; maintain Python BUILD targets explicitly; delete four duplicate libraries and prevent regeneration. Keep uv/pip/runtime and image scripts. | Root `BUILD`, `cli/BUILD`, `tools/BUILD`, `tools/metrics/BUILD`, `gazelle_python.yaml`; corresponding package directives | **Yes: deps/bazel_dep.MODULE.bazel, MODULE.bazel.lock** | **1 rule module**, **5 dedicated metadata/manifest targets**, **4 duplicate libraries** | Medium: BUILD regeneration workflow | Run root Gazelle twice, confirm Python targets do not reappear; build all retained py_binary targets and image script library; compare Go/TS BUILD output. |
| small-query-graph | Replace dagre-d3-react's bundled renderer/layout with a focused graph view; remove its dedicated type chain. Preserve zoom, labels, large-graph guard and graph summaries. | `app/invocation/invocation_query_graph_card.tsx`, `app/invocation/BUILD`, optional new graph implementation/test files there; root manifest/lock | **Yes: package.json, pnpm-lock.yaml** | Up to **833,116 default-bundle bytes**, less replacement; **4 exclusive snapshots**, **6,428,329 unpacked bytes** | Medium/high: layout and interaction parity | Render empty/disconnected/cyclic/1,000-edge graphs; app build; before/after esbuild metafile and compressed bundle size. |
| lightweight-site-globe | Replace the remote-cache globe visual without the WebGL/Three ecosystem; remove react-globe.gl and unused closure. | `website/src/components/globe/globe.tsx`, assets in that component directory; website manifest/lock | **Yes: website/package.json, website/pnpm-lock.yaml** | **45 snapshots**, at least **144,156,716 unpacked bytes**, minus replacement | Medium: visual/product choice; browser-only animation behavior | Frozen website install/build; inspect remote-cache page at desktop/mobile widths; compare install bytes and website route JS. |
| one-diff-engine | Port the one app diff renderer from diff-match-patch to already-used `diff`; retain semantic output and remove old package/types. | `app/util/diff.tsx`, `app/util/BUILD`, focused diff fixtures/tests; root manifest/lock | **Yes: package.json, pnpm-lock.yaml** | **2 records**; **97,445 runtime unpacked bytes + type package**; up to **58,174 default-bundle bytes** before replacement | Medium: algorithms and semantic cleanup differ | Diff fixtures for multiline/Unicode/insert/delete/equal and big logs; app compare UI; metafile size check. |
| memoize-one-align | Use the installed 5.2.1 API for the two terminal memoizers; eliminate direct 6.0.0 rather than adding another memoization library. | Root manifest/lock; reserve `app/terminal/{terminal,text}.tsx` only if API changes needed | **Yes: package.json, pnpm-lock.yaml** | **1 duplicate npm version/record**; exact bundle saving unmeasured | Medium: comparator/reference and perf semantics | Inspect both call signatures; run terminal text/copy tests and scrolling benchmark; prove only one memoize-one version in lock/metafile. |
| k8s-single-caller | Replace the one dev deployment's rules_k8s rendering/apply wrappers with a small maintained command; preserve image resolution and dev command behavior. | `deployment/BUILD`, new deployment render/apply helper; `buildpatches/bazel_dep` k8s patch only | **Yes: deps/bazel_dep.MODULE.bazel, MODULE.bazel.lock** | **1 forked module + kubectl extension**; **14 expanded k8s wrappers replaced**; other Docker/Java/Python modules remain | Medium: existing dev workflow/auth/image substitution | Compare rendered YAML and command arguments; run render/diff only, no apply to a real cluster; re-run mod graph/explain; existing shared image-tag targets must retain behavior. |
| oci-tcpproxy | Converge legacy image/push with the existing OCI equivalents; preserve old public labels via aliases if needed, and choose destinations deliberately. | `tools/tcpproxy/BUILD` | No | **2 duplicated Docker-specific rules/packaging paths**; aliases may leave target count unchanged | Medium: Debian13 Docker base vs Debian12 OCI base, different repo suffix | Build/load both images; compare entrypoint, files, user/permissions and runtime; compare push metadata without pushing. |
| oci-cacheload | Same convergence for cacheload. | `tools/cacheload/BUILD` | No | **2 duplicated Docker rules/paths** | Medium: base/destination parity | Build/load/smoke-test existing OCI image; compare old image content and push metadata, no push. |
| oci-smarter-device-manager | Same convergence for smarter-device-manager. | `tools/smarter_device_manager/BUILD` | No | **2 duplicated Docker rules/paths** | Medium: permissions/device behavior and base/destination parity | Compare image metadata/runfiles; smoke-test command; preserve device-management assumptions; no push. |
| oci-mdload | Same convergence for mdload. | `enterprise/tools/mdload/BUILD` | No | **2 duplicated Docker rules/paths** | Medium: platform compatibility and destination parity | Build supported platform images; command smoke test; compare content/config and push metadata. |
| oci-rbeperf | Same convergence for rbeperf. | `enterprise/tools/rbeperf/BUILD` | No | **2 duplicated Docker rules/paths** | Medium: platform compatibility and destination parity | Build supported platform images; command smoke test; compare content/config and push metadata. |
| oci-codesearch-server | Same convergence for codesearch server. | `codesearch/cmd/server/BUILD` | No | **2 duplicated Docker rules/paths** | Medium: runtime runfiles/base/destination parity | Image startup/config smoke test; compare runfiles, entrypoint, ports and push metadata. |
| oci-probers | Converge old probers image/push with its OCI path; preserve cloudprober/Bazel/BusyBox payload. | `tools/probers/BUILD` | No | **2 duplicated Docker rules/paths**; possible one duplicated Java base image fetch after final reference audit | Medium: external binaries and Java image content | Compare tar file lists, modes, entrypoint and cloudprober smoke invocation; no network deployment/push. |

The task table has **19 rows**. The file reservations avoid non-shared collisions: proto changes are one combined task; code editor type changes touch BUILD only; graph changes touch invocation only; diff changes touch util only; Python generation changes are combined with its duplicate targets. Conditional source fixes in package-only alignment tasks must be coordinated before assigning those files.

## 4. Not worth it / blocked

- **Full clean-build fetch baseline:** blocked by disk capacity, first Chromium then ClickHouse; broader server execution was interrupted before exhausting disk again. Report observed counts and scoped closures, not a fictional full total. Repeat on a larger VM with empty output/repository caches and record fetches before treating the baseline as complete.
- **Unconditional Java/Kotlin/Android/Rust removal:** no first-party rules, but `mod explain` shows transitive owners. Removing direct `rules_proto`, `rules_cc`, abseil, zlib, with_cfg or buildozer declarations does not imply removal of the selected modules. Protobuf/LLVM/Docker upgrades or upstream module-graph cleanup are separate work, not a safe single-script deletion.
- **Python toolchain removal:** requests and packaging have real callers; **7 BUILD files** participate. uv maintains the locked dependencies. Remove generator overhead first, not the runtime.
- **Jasmine reporters:** rejected after reading the rule implementation: it is loaded by the default JUnit reporter. Three exclusive reporter-chain records are not unused just because no application TS imports them.
- **Direct `@types/node` and `@types/long` deletion:** source-import search is insufficient; generated protobufjs and long 4 types consume Node declarations. Long 4 lacks bundled types. Removing these can break generated type checking.
- **Date-library consolidation:** verified overlap, but **19 moment targets across many packages** makes full removal a multi-step effort. A 4.35 MB installed package contributes **155,684 bytes** to this measured app JS. Not represented as a tiny one-file dependency deletion.
- **Monaco removal/trimming:** **9 real importing targets**, 92 MB installed size; enterprise bundle was not built in this disk-limited VM. Language/runtime asset attribution and review/sidekick functionality require an enterprise metafile before recommending exact savings. Do not confuse install size with shipped editor code.
- **libsodium replacement:** one caller but wire-compatible sealed-box encryption is substantive. Browser WebCrypto does not provide a drop-in crypto_box_seal replacement. Keep it and its path-browserify bundling fix unless an equivalent implementation is independently verified.
- **Website Twitter/widget and Docusaurus plugin deletion:** rejected after checking MDX/config. Twitter widget is imported by a BazelCon blog MDX; ideal-image/blog/search/redirect plugins have config/theme callers. A TS-only import scan misses them. The website's 1,312 packages are mostly legitimate framework transitive dependencies.
- **Other-platform Bazel/ClickHouse/VM/runtime binaries:** expanded macros/selects show real consumers; querying all selects is not configured execution. Retiring compatibility versions needs a support-policy decision and binary/test image review, not a literal-label search.
- **Removing all Docker rules now:** still used in executor, CI runner, OSS server, gateway, density, enterprise server/cache paths, test images and deployment. Seven listed convergence tasks do not remove those callers. New OCI bases also use Debian12 while old Docker bases use Debian13; blindly replacing labels changes runtime behavior.
- **Prebuilt protobufjs plugin:** promising standalone frontend build follow-up, but source-only current artifact and cross-platform releases need preparation. It would not remove Go/LLVM from server builds. No independent ready cleanup task or time saving is claimed.
- **Pure-Go flag as frontend C++ fix:** rejected measurement: CppCompile configured count remained **1,488**. Configured action graphs contain toolchain configuration fan-out, so that count is not executed clean-build work. No source-C++ protoc recommendation: protobuf already registers prebuilt protoc.
- **Unused messages in live schemas:** not proven. Reflection, wire clients and persisted data can use messages absent from straightforward name searches; deleting them requires a protocol compatibility audit.

