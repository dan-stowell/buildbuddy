# Dead targets and over-wide first-party dependencies

Research snapshot: `770b88bc89c0b20059d42a237e8c7923ff5b7215` (2026-10-09), surveyed 2026-10-12. No source changes, commits, pushes, or PRs. Evidence and reproducible scripts are in [survey-data](survey-data/commands.md).

## 1. Summary: five biggest wins

1. **Decouple `server/util/ioutil` from `server/interfaces`.** It uses only `Committer` and `CommittedWriteCloser`, whose methods are `Commit`, `Write`, and `Close`. Express these structurally using `io.Writer`, `io.Closer`, and `Commit() error`. The standalone ioutil closure falls from **158 to 3 first-party rules**, and from **89 to 11 external boundary labels**, when that edge is removed. It is reachable from **31 Go binaries**. This is the best small dependency-cut PR; whole-binary savings are unmeasured and could be zero where another path retains interfaces.
2. **Remove the disabled CLI YAML translator.** Its only first-party consumer is its own test. Registration is commented out with “Fix and re-enable,” while JavaScript translation remains enabled. Delete **2 targets, 1 package, 2 Go files / 402 lines**. This saves target/test work, not shipped binary bytes. `yaml_v2` is referenced by 8 BUILD files, so do not remove its external module as part of this task.
3. **Remove three obsolete Go resource bundles, preserving the resources.** `//config:bundle`, `//enterprise/config:bundle`, and `//enterprise:bundle` have **no first-party reverse dependents**. Configs and licenses are consumed through filegroups/data/tars instead. Three disjoint PRs remove **3 targets, 3 Go packages, 49 Go lines**. The last change to these Go files was the 2023-08-09 “Break up bundles” change.
4. **Delete the old RBE version-prefix helper and unused pubsub test fake.** Neither has a first-party consumer, including tests. Together: **2 targets, 2 packages, 117 Go lines**. The RBE rollout helper's substantive history ends in April 2022; the fake's last behavioral fix was August 2023.
5. **Split health-check types out of the interfaces catch-all.** Healthcheck imports only `Checker` and `CheckerFunc` from a target with **53 direct dependencies**, used directly by **288 rules** and transitively by **44 Go binaries**. Move those types into a leaf package and alias them from interfaces to preserve type identity. The simulated cut removes **152 first-party rules and 70 external boundary labels** from healthcheck's standalone closure; healthcheck reaches **24 Go binaries**. This is a larger PR than the deletions, adds a leaf target, and needs compatibility checks.

The six established dead-package candidates above total **7 deletable targets, 6 packages, 7 Go files, 568 lines**. Two more self-only packages are recent additions: deleting those too would bring the total to **11 targets, 8 packages, 11 Go files, 914 lines**, but they require an owner decision about pending integration. None of these deletions has a demonstrated external-module or binary-size saving. There are also **9 unread flag registrations** worth small cleanup PRs; they do not guard removable implementation branches.

## 2. Measurements

### Baseline

Commands:

```sh
bazel query //... --output=label_kind
awk '{print $1}' survey-data/targets.label_kind | sort | uniq -c | sort -nr
python3 survey-data/static_go.py
```

Trimmed output:

```text
5043 total rules under //...
1162 filegroup
 693 go_library
 628 _build_test
 446 npm_package_store_internal
 403 go_test
 314 ts_project
 314 swc_compile
 107 proto_library
 107 go_proto_library
  83 go_binary
  82 js_library
  79 protoc_gen_protobufjs
  65 alias
  16 container_image_
  12 container_push_
  10 oci_image
   7 oci_push
   8 py_binary
   4 sh_binary
Go directories 709; Go files 1198; production Go directories 669
```

Full [rule-kind counts](survey-data/rule-kind-counts.txt) and [target inventory](survey-data/targets.label_kind) are retained. Counts include macro-generated targets and npm stores under `//...`; they are not counts of handwritten rules. `git ls-files '*.go'` independently gives 1,198 files in 709 directories. Excluding `third_party/singleflight` gives **1,197 first-party Go files, 708 directories, 668 directories with non-test Go files**. These are source-directory package counts, not a successful `go list ./...`; generated proto packages and build-tag variants are not added.

### Roots and their meaning

Commands:

```sh
rg -n 'bazel (build|run|test)|//[^ "\x27]+|buildkite' .github/workflows release.py tools --glob '*.yml' --glob '*.yaml' --glob '*.sh' --glob '*.py'
rg --files | rg '(buildkite|pipeline|experiment|migration)'
bazel query 'kind("go_binary|.*image.*|.*push.*|.*deploy.*", //...)' --output=label_kind --bes_backend=
```

[Root-reference search](survey-data/root-reference-search.txt), [binary/image inventory](survey-data/executable-image-roots.label_kind), and both calculated root lists are retained.

| Root source | Targets/patterns found |
| --- | --- |
| `.github/workflows/main.yaml`, `pr.yaml`, `coverage.yaml` | `bazelisk test //...`; CI test filters exclude some tagged tests. No `build_tests_only` setting was found. |
| `buildbuddy.yaml` | Linux and macOS `test //...`; Linux ARM remote-execution/integration subsets; `//tools/lint`; explicit release binaries and images. |
| GitHub release artifact workflows | FOSS/enterprise BuildBuddy, executor, static Linux executor variants, Windows executor aliases; macOS/Windows/Linux platform variants. |
| `release.py:234–255` | `//server/cmd/buildbuddy:buildbuddy_image`, `//enterprise/server/cmd/server:buildbuddy_image`, `//enterprise/server/cmd/executor:executor_image`, `//enterprise/server/cmd/cache_proxy:cache_proxy_image`. |
| `.github/workflows/release-cli.yaml` | `//cli/cmd/bb`, across release platforms. |
| Website workflows | `//website:website`; Helm workflow uses Helm rather than an additional Go entry point. |
| Other checked-in executables/images | Codesearch server/CLI, DNS density, gateway/relaygateway, atlas, cache, metadata server, ci_runner, goinit, probers, operational tools and dashboard generators. Retained conservatively as roots. |
| Deployment BUILD files | Kubernetes apply/create/diff/replace/delete/resolve wrappers under `//deployment`, image pushes under `//enterprise/deployment`. |
| `tools/` | Lint, documentation/generator and maintenance commands; `populate_tests.sh` also runs `test //...`; Firecracker installation builds executor Firecracker/jailer targets. |

No Buildkite pipeline config was found in tracked files; the Buildkite hits were static images. Private deployment repos and operator usage are unavailable. Having no root other than **broad `//...` CI** is therefore not enough to call a tool dead. Every Go binary was retained as a root even if its deployment was unknown.

### Reachability: successful measurements and failed unrestricted queries

The intended unrestricted comparison was:

```sh
bazel query 'kind("go_library", //...) except deps(kind("go_binary", //...))' --output=label --bes_backend=
```

It expanded to **2,285 loaded packages** and was interrupted. Full-universe `rdeps(//..., ...)` also expanded into external repositories and was interrupted. A narrower Go-rule-universe query with `--noimplicit_deps --notool_deps` failed with exit **7**:

```text
An error occurred during the fetch of repository
'+install_static_dependencies_ext+io_bazel_bazel-9.2.0-linux-x86_64'
Error in download: java.io.IOException: No space left on device
... referenced by '//server/testutil/testbazel:bazel-9.2.0_crossplatform'
... preloading transitive closure failed
```

These were **not successful empty queries**. [Failure log](survey-data/go-rdeps-failure.stderr.txt) and interruption logs are retained. Disposable repository-cache entries created during the survey were removed to recover disk space.

Successful alternative:

```sh
bazel query //... --output=xml --bes_backend= > /tmp/bb-targets.xml
python3 survey-data/analyze_graph.py
```

The [compressed XML snapshot](survey-data/targets.xml.gz) contains all 5,043 first-party rules and their `rule-input` edges, including implicit edges and the query's union of `select()` branches. The script follows those edges **within first-party rules**. It does not require downloading external transitive closures. This measures first-party reachability, not configured action graphs or external-to-first-party re-entry. It does not inspect Starlark source loads as runtime consumers.

```text
first-party rules 5043
binary roots 83
conservative shipping/image/deploy roots 82
all-binaries reached rules 1835; reached libraries 595; unreached libraries 98
shipping reached rules 1843; reached libraries 530; unreached libraries 163
```

The shipping set includes image/push/Kubernetes rule kinds, non-test `/cmd/` Go entry points, CLI and app/website bundles. The binary set additionally preserves every developer tool and generator. Neither count is a dead-code count: most unreached libraries are useful test infrastructure. Full lists: [outside all Go binaries](survey-data/all-binaries-unreached-go-libraries.txt), [outside conservative shipping roots](survey-data/shipping-unreached-go-libraries.txt).

Exact transitive first-party reverse dependencies from the XML:

| Library | All first-party reverse dependencies, including itself |
| --- | --- |
| `//cli/translate/yaml:yaml` | itself, `//cli/translate/yaml:yaml_test` |
| `//config:bundle` | itself |
| `//enterprise/config:bundle` | itself |
| `//enterprise:bundle` | itself |
| `//enterprise/server/util/rbeutil:rbeutil` | itself |
| `//server/testutil/pubsub:pubsub` | itself |
| `//enterprise/server/util/git_trace2:git_trace2` | itself, `//enterprise/server/util/git_trace2:git_trace2_test` |
| `//enterprise/server/testutil/exptest:exptest` | itself, `//enterprise/server/testutil/exptest:exptest_test` |

An additional **successful Bazel query** selected direct `deps` or `embed` consumers of exactly these labels, without transitive external fetching. It returned only:

```text
go_test rule //cli/translate/yaml:yaml_test
go_test rule //enterprise/server/testutil/exptest:exptest_test
go_test rule //enterprise/server/util/git_trace2:git_trace2_test
```

Exact command and output: [direct-consumer evidence](survey-data/dead-package-direct-consumers.txt), [commands](survey-data/commands.md). The XML check covers other input attributes too, rather than assuming deps/embed are the entire graph.

### Directory history and corroboration

Commands: `git log --format='%h %cs %s' -- <directory>`; `rg` for candidate import paths and translator registration. Outputs are retained in `survey-data/*-history.txt`.

| Candidate | Last relevant history / corroboration |
| --- | --- |
| `cli/translate/yaml` | Last substantive translator feature: `848ccdf5e`, 2023-11-14, MODULE.bazel support. Later changes are API/style modernizations, most recently 2026-09-10. `cli/translate/translate.go:20` has commented-out YAML registration; BUILD depends only on JS. Graph proves it still does not ship. |
| `enterprise/server/util/rbeutil` | `8b4a02d19`, 2022-04-08 removed the v2 prefix requirement; April 2022 followups moved flag handling; last directory edit `f3fec1905`, 2022-10-26 changes visibility. No consumer remains. |
| `server/testutil/pubsub` | `6b5ac36c3`, 2023-08-10 fixes fake shutdown behavior. No caller, including tests. |
| Three `bundle.go` files | `b9b82d843`, 2023-08-09 “Break up bundles.” No Go consumer; configs/licenses still appear in shipping BUILD data/filegroups. Delete wrappers, not resource directories. |
| `enterprise/server/util/git_trace2` | Added `e8ac9cc2a`, 2026-07-15; self-test only. Recent unused utility, not evidence of an abandoned service. |
| `enterprise/server/testutil/exptest` | Added `5c4524e5b`, 2026-10-01. `testonly = True`; self-test only. Particularly likely to be awaiting adoption. |

### Over-wide dependencies

Commands:

```sh
python3 survey-data/analyze_graph.py
python3 survey-data/measure_candidates.py
rg -n 'interfaces\.' server/util/ioutil/ioutil.go server/util/healthcheck/healthcheck.go
```

Trimmed output:

```text
//server/interfaces:interfaces       direct consumers 288; Go binary fanout 44; direct deps 53
//server/environment:environment     direct consumers 173; Go binary fanout 40; direct deps 13
//server/real_environment:real_environment direct consumers 134; Go binary fanout 30; direct deps 13

ioutil: interfaces.CommittedWriteCloser; interfaces.Committer
healthcheck: interfaces.Checker; interfaces.CheckerFunc
```

`interfaces.go` combines auth, DB/GORM, DNS, Serf, GitHub, long-running-operation APIs, cache/execution, and dozens of generated protos. Those are real imports; deleting deps arbitrarily would fail strict-deps checks. Extracting leaf contracts or using local structural interfaces cuts the coupling.

Simulate removing only the named util → interfaces edge:

| Package | First-party rules before → after | External boundary labels before → after | Go binary fanout |
| --- | --- | --- | --- |
| ioutil | 158 → 3 | 89 → 11 | 31 |
| healthcheck | 169 → 17 | 94 → 24 | 24 |
| expflag | 157 → 16 | 89 → 39 | 12 |
| disk | 171 → 171 | 111 → 111 | 22 |
| compression | 159 → 159 | 90 → 90 | 30 |

[Raw measurements](survey-data/candidate-metrics.txt). These simulations establish the cost of an edge, **not** that the proposed implementation has built. A healthcheck leaf adds a target to the after-count. Boundary labels identify directly referenced external targets, not external module counts or recursively expanded repositories. Savings across these rows overlap and must not be summed.

The scan of declared **first-party go_library deps versus actual source import paths found zero unexplained edges**. It is not a complete check of external deps, generated sources, or embeds. Largest library dependency counts include `server_lib` 82, `buildbuddy_server` 75, `libmain` 61 and `executor_lib` 61. These are actual composition roots; high counts alone do not justify splitting them. Interfaces is the actionable catch-all because leaf consumers use only two methods/types.

### Unused flags and feature gates

Commands:

```sh
python3 survey-data/scan_flags.py
rg -n '\b(defaultTerminationGrace|labelFile|userManagementEnabled|newTrendsUIEnabled|traceViewerEnabled|useV2Layout|awsS3DisableSSL|transientStore|disableSSL)\b' server enterprise --glob '*.go'
```

The discovery scan finds **23 declarations with one identifier occurrence in their own non-test package**. Manual inspection rejects exported variables used by other packages and an example in a comment. The following **9 registrations have no code reader** in their defining package; these variables are unexported:

| File | Flags / evidence |
| --- | --- |
| `server/static/static.go` | `app.user_management_enabled`, `app.new_trends_ui_enabled`, `app.trace_viewer_enabled`: 3 declarations, zero identifier readers. First is explicitly no-effect; second deprecated. |
| `server/backends/blobstore/disk/disk.go` | `storage.disk.use_v2_layout`: declaration only. Do not confuse with the live, separate disk-cache layout implementation. |
| `enterprise/server/remote_execution/executor/executor.go` | `executor.default_termination_grace_period`: declaration only. |
| `enterprise/server/remote_execution/containers/podman/podman.go` | `executor.podman.transient_store`: declaration only; help says transient store is applied automatically based on version. |
| `server/backends/blobstore/aws/aws.go` | `storage.aws_s3.disable_ssl`: declaration only, deprecated; endpoint scheme replaces it. |
| `enterprise/server/backends/s3_cache/s3_cache.go` | `cache.s3.disable_ssl`: declaration only, deprecated; endpoint scheme replaces it. |
| `enterprise/server/backends/prom/generate_docs/generate_docs.go` | `label_file`: declaration only. |

[Reference output](survey-data/flag-reference-evidence.txt). Podman's dynamic flag lookups read `executor.docker_network`, `docker_cap_add`, `executor.docker_devices`, and `executor.docker_volumes`, not the unused flag. No matching dynamic getter was found in the other candidate packages. Registering a flag still affects accepted config/arguments, so removal is a compatibility change even without a code reader. Absence of dotted names in checked-in configs does not establish absence in private YAML.

No live feature gate was proven **always on/off in every deployment**. True/false defaults are not proof. These findings remove registrations and stale descriptions, not feature implementations.

### Build verification limit

No full `bazel build //...` was attempted after the query ran out of disk. Instead:

```sh
timeout --signal=INT --kill-after=10s 90s bazel build //enterprise/server/util/rbeutil --bes_backend= --remote_cache= --remote_executor=
```

Result:

```text
... 98,328 targets configured, 158 aspect applications ...
[423 / 1,976] Compiling libcxx/src/condition_variable.cpp [for tool] ...
ERROR: build interrupted
INFO: Elapsed time: 90.152s, Critical Path: 5.36s
INFO: 429 processes: 249 internal, 180 linux-sandbox.
ERROR: Build did NOT complete successfully
exit_code=124
```

Even the tiny stdlib-only helper encountered substantial toolchain work under this repository's defaults. [Build log](survey-data/narrow-build.stderr.txt). No source changes were made, so there is no deletion/build-pass claim. Clean-build seconds, linked bytes and removed external modules remain unmeasured; proposed verification below belongs to each implementation PR on a provisioned builder.

## 3. Candidate tasks

Ranked by measured gain versus estimated effort. Each row touches distinct files; the healthcheck split owns the interfaces files. **No row requires shared dependency-file edits** (`go.mod`, MODULE, deps.bzl, WORKSPACE, package.json, lockfiles). Removing a local BUILD edge does not establish that an external module can be removed globally. For deletion verification, compare the saved graph before/after, query remaining callers, then run the indicated builds/tests; do not try to run deleted tests.

| slug | what to do | files/dirs it touches | touches shared dep files? | expected gain (numbers) | risk | how to verify |
| --- | --- | --- | --- | --- | --- | --- |
| ioutil-leaf-contracts | Replace the two interfaces references with equivalent local structural interfaces; drop interfaces BUILD dep. | `server/util/ioutil/{ioutil.go,BUILD}` | No | Direct deps 3→2; standalone closure −155 first-party rules / −78 external boundary labels; 31 binary consumers. | Low–medium: preserve Commit/Close behavior and exported signatures' assignability. | Test `//server/util/ioutil:ioutil_test`; build first-party reverse dependents and CLI/server/executor; rerun closure measurement. |
| delete-yaml-translator | Delete unused YAML implementation, its test and BUILD. Leave disabled registration comment for a separate decision so this task does not edit live translator files. | `cli/translate/yaml/` | No | −2 targets, −1 package, −402 Go lines; 0 shipped binary/module savings demonstrated. | Low: dormant prototype may have external consumers despite current CLI not using it. | Confirm only own-test rdeps; build `//cli/cmd/bb`; run CLI tests and smoke-test JS translation. |
| delete-foss-config-bundle | Delete `bundle.go` and just the `bundle` rule; retain config filegroup/exported YAML. | `config/{bundle.go,BUILD}` | No | −1 target/package, −17 Go lines. | Low: keep release/non-release resource selection. | Query remaining config filegroups; build FOSS binary/image; compare packaged config files. |
| delete-enterprise-config-bundle | Delete Go wrapper and bundle rule; retain all YAML/filegroups. | `enterprise/config/{bundle.go,BUILD}` | No | −1 target/package, −17 Go lines. | Low: resource glob must retain same shipping YAML set. | Build enterprise server/executor/metadata-server; compare config runfiles/image contents. |
| delete-enterprise-license-bundle | Delete Go license wrapper and bundle rule; retain `licenses` filegroup and LICENSE. | `enterprise/{bundle.go,BUILD}` | No | −1 target/package, −15 Go lines. | Low: license files must continue to ship. | Build enterprise release binaries/images; verify LICENSE runfiles and packaged files. |
| delete-unused-pubsub-fake | Delete unused test fake and BUILD. | `server/testutil/pubsub/` | No | −1 target/package, −92 Go lines; no consumers even in tests. | Low. | Check full first-party rdeps and source references; run server/enterprise pubsub and quota tests on a normal CI builder. |
| delete-rbe-v2-helper | Delete obsolete execution-ID rollout helper and BUILD. | `enterprise/server/util/rbeutil/` | No | −1 target/package, −25 Go lines; 0 code users. | Low: do not remove live execution-ID handling elsewhere. | Confirm no callers; build executor and enterprise server; run execution/pubsub tests. |
| healthcheck-leaf-types | Move Checker/CheckerFunc into a leaf type package; retain aliases in interfaces; point healthcheck at leaf. | `server/interfaces/{interfaces.go,BUILD}`, `server/util/healthcheck/{healthcheck.go,BUILD}`, new health type package | No | Simulated −152 first-party rules / −70 external boundary labels from standalone healthcheck, before adding leaf; 24 binary consumers; +1 leaf target. | Medium: exact method signatures and CheckerFunc identity must remain compatible. | Test healthcheck, testenv and signal/graceful-shutdown cases; build all reverse dependents and shipping Go roots; compare closures. |
| remove-static-noop-flags | Remove three unused static/UI flag registrations. | `server/static/static.go` | No | −3 flags/registrations; no targets or branch code eliminated. | Medium: reject formerly accepted private config keys unless compatibility is handled. | Search config keys and dynamic lookups; test static/client-config generation; load representative production config. |
| remove-blobstore-layout-flag | Remove unused disk-blobstore flag only. | `server/backends/blobstore/disk/disk.go` | No | −1 flag/registration; disk-cache migration code gain 0. | Low–medium: old config compatibility. | Test blobstore disk; confirm disk-cache layout tests unchanged; parse representative config. |
| remove-unused-executor-grace-flag | Remove unread default termination-grace registration. | `enterprise/server/remote_execution/executor/executor.go` | No | −1 flag/registration, 0 proven branch savings. | Medium: advertised setting may mislead users; actual platform timeout behavior must be preserved. | Test executor timeout/termination cases; inspect private executor configs for key use. |
| remove-podman-transient-flag | Remove deprecated unread flag; preserve automatic version-based transient-store behavior. | `enterprise/server/remote_execution/containers/podman/podman.go` | No | −1 flag/registration. | Low–medium: stale configs; Podman version behavior. | Test Podman command construction/integration, both supported and unsupported transient-store versions. |
| remove-aws-blobstore-ssl-flag | Remove deprecated unread SSL flag; endpoint scheme remains authoritative. | `server/backends/blobstore/aws/aws.go` | No | −1 flag/registration, 0 external modules. | Medium: private configs/MinIO setup. | Run AWS blobstore endpoint tests and HTTP/HTTPS MinIO smoke tests; check config compatibility. |
| remove-s3-cache-ssl-flag | Remove deprecated unread cache S3 SSL flag. | `enterprise/server/backends/s3_cache/s3_cache.go` | No | −1 flag/registration, 0 external modules. | Medium: private configs; endpoint behavior. | Run S3 cache tests and HTTP/HTTPS endpoint smoke tests; check config compatibility. |
| remove-prom-doc-label-flag | Remove unused docs-generator label-file argument registration. | `enterprise/server/backends/prom/generate_docs/generate_docs.go` | No | −1 flag/registration. | Low: check caller command lines before removing accepted argument. | Search label_file invocations; run generator and compare generated docs byte-for-byte. |
| delete-unintegrated-git-trace2 | If owner confirms integration is canceled, delete utility/tests/BUILD; otherwise retain as pending work. | `enterprise/server/util/git_trace2/` | No | −2 targets, −1 package, −267 Go lines; only own test uses it. | Medium: added 2026-07-15; could be intended for a followup. | Confirm owner intent and only-own-test rdeps; build enterprise roots; run git/ci-runner tests. |
| delete-unadopted-exptest | If owner confirms test-helper adoption is canceled, delete helper/tests/BUILD. | `enterprise/server/testutil/exptest/` | No | −2 targets, −1 package, −79 Go lines. | High relative to tiny gain: added 2026-10-01. | Confirm owner intent; query all tests for callers; run experiments/expflag tests. |

The last two rows are held candidates, not recommendations to delete new code simply because adoption has not happened yet. The seven flag cleanup rows account for 9 flags. Preserve a deprecated no-op registration instead if backwards compatibility requires it; that choice yields no registration-count saving.

## 4. Not worth it / blocked

- **Delete all 98 libraries outside Go binaries:** rejected. Most are test utilities/fixtures, including integration RBE harnesses, mock registries and test environments. CI tests are legitimate consumers. Only the eight self-only/no-consumer libraries above passed the stricter first-party reverse-dependency check.
- **Delete experimental or migration trees based on names:** rejected. `migration_cache` is explicitly a dependency of both enterprise server and cache binaries. `migration_operators` is imported by live routing AC/CAS clients. Experiments and execution experiment flags have shipped consumers. No abandoned whole service was established.
- **Remove all config or enterprise directories:** rejected. Only the Go wrappers are dead; configs/licenses remain image/runfile inputs. These tasks deliberately retain those resources.
- **Remove disk/compression → interfaces edges in isolation:** rejected as a build-shrink claim. Simulations show **0 reduction** in their closures because another dependency path retains interfaces. Such a cleanup might follow ioutil decoupling, but is not an independent measured win now.
- **Split expflag immediately alongside healthcheck:** measurable potential (−141 first-party rules / −50 external boundary labels), but both type extractions edit `server/interfaces`. Kept out of the parallel backlog to avoid overlapping PRs. Re-measure after the healthcheck task and establish a separate ownership window.
- **Split environment/real_environment wholesale:** broad fanout is real, but these expose many service interfaces. Not a small independent task without a consumer-by-consumer design. No blanket interface or dependency deletion recommended.
- **Delete large service composition libraries:** rejected. `server_lib`/`libmain`/executor composition dependencies match real source imports; count alone is not evidence of waste.
- **Remove `ci_runner`'s unread `digest_function` flag:** identifier is declaration-only, but hostedrunner and workflow service explicitly send `--digest_function=BLAKE3`. Removing only the registration breaks callers. [Name-reference evidence](survey-data/additional-flag-name-references.txt). Low-value coordinated compatibility cleanup, excluded from independent rows.
- **Exported “unused” flags:** false positives from a package-local identifier scan. Examples include feature/config variables, execution experiment exports, snaputil settings, RPC OTEL setting and remote-cache read buffer. Other packages read them. The example `widgetSize` is a comment. Not deletion candidates.
- **Always-on/off gates:** private configs and runtime experiment providers prevent proof from defaults alone. No such branch-removal claim is made.
- **Unused standalone tools:** all existing executable targets were preserved. No checked-in deployment reference does not prove nobody runs an operational binary manually.
- **Full graph/build and binary-size verification:** blocked by external fetch disk exhaustion and narrow-build timeout. First-party graph measurements succeeded; unrestricted rdeps and build success did not. No clean-build speedup in seconds, binary bytes, or globally removable external modules was established.

