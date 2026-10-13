# Findings

What the coordinator's Codex workers were asked to do on this buildbuddy fork, what they found, and how each task ended.
`ledger.md` is the authority on outcomes and PR numbers. Where it disagrees with a worker's own report, this file follows the ledger and says so.
All numbers come from the records linked in each entry. Full Codex transcripts stay on the coordinator VM under `coord/transcripts/`.

## Contents

1. [ext4 conversion IO](#ext4-conversion-io)
2. [Cache proxy](#cache-proxy)
3. [Metrics and design docs (stopped)](#metrics-and-design-docs-stopped)
4. [Build simplification](#build-simplification) (Go deps, dead code, protos, OCI rules, frontend)
5. [Test speed](#test-speed) (surveys, waves 1–3, harness)
6. [OCI fetcher split plan](#oci-fetcher-split-plan)
7. [Lessons](#lessons)

PR links point to `https://github.com/dan-stowell/buildbuddy/pull/N`. As of 2026-10-13, no PR from this work had merged (ledger, Threads).

---

## ext4 conversion IO

The goal was to cut disk IO when `ConvertContainerToExt4FS` (`enterprise/server/oci/ociconv/ociconv.go`) turns an OCI image into an ext4 image for Firecracker. The baseline pipeline writes at least 2x the rootfs and reads it back at least once: tar extract to an unpack dir, then `mke2fs -d` over that dir, then a filecache add. Three workers took three angles: stream straight to ext4, tune mke2fs, and audit everything around the conversion. Each opened a PR. A reviewer approved all three but listed missing tests, so three `gaps-*` workers added tests and pushed them to the same branches. coord-notes keeps only the ledger rows and task specs for this effort: no result.md, review or diff. The measured IO savings are therefore not recorded here (see the transcripts). Per the ledger, PRs #6–#8 are "still open, untouched". The follow-up metrics task (metrics-ext4) was stopped.

*These workers ran before transcript backups existed. Their measurements and verification are in the PR descriptions: #6, #7, #8.*

### ext4-stream
- **Asked:** Build the ext4 image straight from the flattened tar stream (`mke2fs -d <tarball>`, hcsshim `tar2ext4`, or similar) so no unpack dir is written, and benchmark it against the baseline on alpine, ubuntu and a ≥1 GB image.
- **Found:** No result.md is kept here. The gaps-stream spec shows what the PR contains: a streaming path that falls back to the baseline when not root, a benchmark tool at `enterprise/tools/oci_ext4_benchmark/` with `compare.py`, and an OCI streaming integration test.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/8.
- **Review / gaps-stream:** The review approved the PR but found these gaps, which gaps-stream filled and pushed to `t-ext4-stream`: assert the integration test really takes the streaming path, so it can't pass silently through the baseline; automated baseline-vs-streaming checks for sparse files, long or PAX names, xattrs and fractional mtimes, which had relied on the benchmark fixture; and hardlink equivalence for symlinks and special files (`compare.py:28` grouped only regular files).
- **Records:** [tasks/ext4-stream.md](tasks/ext4-stream.md), [tasks/gaps-stream.md](tasks/gaps-stream.md).

### ext4-mkfs
- **Asked:** Keep the unpack-dir pipeline but cut mke2fs IO: lazy init and sparseness, dropping the journal if the guest mounts read-only, sizing slack (currently 1.2x plus 4 MB), fsync behaviour, and putting the unpack dir on tmpfs.
- **Found:** No result.md is kept here. Per the gaps-mkfs spec, the PR adds a tmpfs unpack dir guarded by a `fitsTmpfs` size check, with a retry/fallback to disk when tmpfs runs out of space.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/6.
- **Review / gaps-mkfs:** Every test case could pass through the disk fallback, so the tmpfs path was never proven. gaps-mkfs added a statfs-magic assertion and an exhaustion/fallback test (root only), a cancellation test after partial extraction (cleans up mounts and leaves no partial image), and direct `fitsTmpfs` tests (manifest errors, multi-layer totals, the exact-limit boundary). Pushed to `t-ext4-mkfs`.
- **Records:** [tasks/ext4-mkfs.md](tasks/ext4-mkfs.md), [tasks/gaps-mkfs.md](tasks/gaps-mkfs.md).

### ext4-audit
- **Asked:** Find redundant IO around the conversion: layer fetch and caching, filecache `AddFile` copy vs rename, the legacy `hashFile` re-read, post-conversion copies, and duplicate conversions caused by the singleflight key including credentials. Fix the cheap wins.
- **Found:** No result.md is kept here. Per the gaps-audit spec, the PR adds a per-image conversion lock that ignores credentials. Concurrent callers with valid and invalid credentials now share one conversion.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/7.
- **Review / gaps-audit:** gaps-audit added tests for mixed invalid/valid concurrent callers in both orders (an auth failure must not fail the valid caller), for a caller cancelled while waiting on the lock (returns promptly while the holder's conversion still completes), and for credential-blind serialization in legacy-cache mode. Pushed to `t-ext4-audit`.
- **Records:** [tasks/ext4-audit.md](tasks/ext4-audit.md), [tasks/gaps-audit.md](tasks/gaps-audit.md).

---

## Cache proxy

Two specs came from an unverified code read of `byte_stream_server_proxy.go`. Both told the worker to prove the behaviour with a failing or RPC-counting test before fixing anything, and to stop and write up the truth if the claim was wrong. Both opened PRs, so the ledger implies the premises held, but the verification evidence isn't kept in coord-notes. Per the ledger, PRs #9 and #10 are "still open, untouched". The observe-only metrics-proxy task was written to measure how often each case happens in production. It was stopped before a PR.

*These workers ran before transcript backups existed. Their measurements and verification are in the PR descriptions: #9, #10.*

### proxy-splitblob
- **Asked:** Verify and fix two claims. (1) For blobs over ~4 MB, the chunked read path calls remote `SplitBlob` before checking the local cache, which wastes a remote RPC on every local hit of a never-chunked blob. (2) `writeChunked` never stores the chunked manifest locally, so reads of blobs uploaded through the proxy also go remote. Fix behind a default-off flag without skipping any auth check that the remote call provided.
- **Found:** Not recorded in coord-notes (ledger: "verify+fix proxy #1/#3").
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/10.
- **Records:** [tasks/proxy-splitblob.md](tasks/proxy-splitblob.md).

### proxy-partial-read
- **Asked:** Verify and fix the claim that an offset/limit read that misses locally streams the partial data into a full-blob local write. That write then fails its size/digest check, so the disk writes are wasted and repeat reads keep going remote. The fix is either to skip the write, or to fetch the whole blob below a size threshold. Compressed reads must be handled.
- **Found:** Not recorded in coord-notes (ledger: "verify+fix proxy #2").
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/9.
- **Records:** [tasks/proxy-partial-read.md](tasks/proxy-partial-read.md).

---

## Metrics and design docs (stopped)

These four ran after PRs #6–#10. The user stopped all four to refocus on build simplification. The two metrics tasks finished working code (observe-only Prometheus metrics, extra request-path work behind default-off flags), but no PR was opened. The patches are saved in `archive/`. The ledger lists handoff-design as stopped, yet its archived result is a complete design doc. rewind-design saved nothing.

### metrics-ext4
- **Asked:** Add observe-only metrics to decide which ext4 conversion optimizations are worth shipping: conversions vs cache hits, re-conversion after eviction, credential-overlap duplicates, per-phase IO and duration, image size, wait time, and the mke2fs version.
- **Found:** Commit `a95f1519b` adds 9 new metrics (conversions, cache lookups, reconversions, credential overlap, deduped waiters, phase duration and IO, image size, mke2fs version) plus a new `image_conversion` stage in the existing firecracker stage histogram. Three default-off flags gate the extra work: a 4096-key LRU of recent conversions, `/proc/self/io` sampling, and image-size stat. The existing filecache metrics are aggregates across all artifacts and can't isolate OCI conversions. Uncached tests passed as root. `TestDirectoryToImageAutoSize_Reproducible` fails as a known, pre-existing issue on e2fsprogs 1.47.0 (the reproducibility flag needs ≥1.47.2). The Firecracker tests could not be built without root or `--config=remote`, so they are unverified.
- **Outcome:** Stopped (user refocused on build shrink). Code is saved, with no PR.
- **Records:** [archive/metrics-ext4-result.md](archive/metrics-ext4-result.md), [archive/metrics-ext4.patch](archive/metrics-ext4.patch), [tasks/metrics-ext4.md](tasks/metrics-ext4.md).

### metrics-proxy
- **Asked:** Add observe-only metrics for proxy↔app IO decisions: remote RPCs and bytes split by local hit/miss, wasted `SplitBlob` NotFound calls, partial-read misses and failed commits, AC revalidation outcome and entry age, FindMissing reuse within 1/5/15 minutes, and zstd compressibility.
- **Found:** Commit `ffe6c1e89` adds `buildbuddy_proxy_remote_rpcs` and `_remote_bytes` with a `cache_status` label, along with `split_blob_not_found_local_hits`, `partial_read_local_misses`, `partial_read_local_writes` and others. It also has three default-off flags: FindMissing reuse uses an LRU capped at 100,000 keys, AC age does an extra metadata lookup, and zstd sampling covers 1 in N blobs up to 1 MiB. Existing metrics could not pair a SplitBlob NotFound with a later local hit, and had no partial-read commit metric. All six test targets passed uncached.
- **Outcome:** Stopped (user refocused). Code is saved, with no PR.
- **Records:** [archive/metrics-proxy-result.md](archive/metrics-proxy-result.md), [archive/metrics-proxy.patch](archive/metrics-proxy.patch), [tasks/metrics-proxy.md](tasks/metrics-proxy.md).

### handoff-design
- **Asked:** Write a design doc, with no PR, on making hinted handoffs in the distributed cache never drop: read the owner's open PRs, list every remaining loss path, and propose mechanisms, costs and a plan.
- **Found:** The relevant PRs are upstream: #13745 (queue a hint on the already-exists path), #13746 (requeue, shutdown drain/forward, event metrics) and #13741 (reconnect backoff; despite its "1s" title, the fetched code's default cap is 5 s). The fork had zero open PRs by dan-stowell. Combined with master, the PRs still don't make handoff durable: there are 14 remaining loss paths (L1–L14), including queue overflow, holder process loss, ack before durability, local-holder bypass, eviction of the hinted data, and membership changes. Hints alone cannot survive the loss of the holder's disk while the target is down. The recommendation is to take the PR mitigations, then build a durable, quota-bounded CAS handoff spool that keeps the payload and fails writes it can't protect, and to measure before paying for replicated spools or anti-entropy.
- **Outcome:** Stopped per the ledger. The archived result reads as a finished design doc. No code.
- **Records:** [archive/handoff-design-result.md](archive/handoff-design-result.md), [tasks/handoff-design.md](tasks/handoff-design.md).

### rewind-design
- **Asked:** Find out whether Bazel action rewinding (lost-input recovery) lets the cache relax its blob-presence guarantees, citing Bazel source, and say which BuildBuddy mechanisms could be relaxed for which clients.
- **Found:** Nothing saved (the archived result and patch are empty). The ledger notes that the cache topology is only client→proxy→app, so any doc that assumed proxy chains would need correcting.
- **Outcome:** Stopped (user refocused). No usable output.
- **Records:** [tasks/rewind-design.md](tasks/rewind-design.md), [archive/rewind-design-result.md](archive/rewind-design-result.md) (empty).

---

## Build simplification

Three surveys (Go deps, non-Go externals, dead code) produced backlogs. Two dispatch waves followed. 15 PRs are open (#18–#47). Five tasks ended without a PR: three were dropped because preserving behaviour wasn't possible (yaml v3, diff engine, uuid), one was dropped after review found UX regressions (promptui), and one was stopped by the user mid-fix (query graph). Most Go swaps remove first-party import and BUILD edges only. Deleting the module declarations is deferred to one dep-prune PR, which the user approved on 2026-10-13, to run after the source PRs merge (#18, #35, #20, #29, #42, #40, #36; dagre-d3-react only if the graph lands). Neither the surveys nor the workers measured binary size or clean-build time: full builds and repo-wide queries kept failing on disk space or Docker fetch timeouts. Decisions recorded in the ledger: keep `git_trace2` and `exptest`, leave unread flags alone, and drop build speed and remote execution as goals.

*Surveys*

### shrink-go
- **Asked:** Survey every Go module: importer counts, small swaps to stdlib or existing deps, duplicate libraries, heavy transitive trees, and test-only deps.
- **Found:** Baseline: 178 direct modules, 257 indirect, 457 generated Go repository rules. The top wins were jwx→golang-jwt in selfauth (up to 7 modules), survey→promptui (up to 3), SendGrid SDKs→HTTP (2 direct), cAdvisor→sysfs (1 direct; its 90-module graph has 0 exclusive indirect modules), and yaml.v2 callers (0 guaranteed repo removals, since 14 selected modules still need v2). Rejected: SQLite consolidation (two build variants), VictoriaMetrics (Dragonboat imports it), AWS v1 (Tink awskms needs it), the gobwas glob swap (brace syntax), pkg/errors (exported StackTrace types), and UUID/semver/retry rewrites. A narrow Bazel build timed out at 120 s.
- **Outcome:** Done, survey only.
- **Records:** [surveys/shrink-go.md](surveys/shrink-go.md), [workers/shrink-go/result.md](workers/shrink-go/result.md).

### shrink-ext
- **Asked:** Survey non-Go externals: MODULE/rule sets/toolchains, npm, and protos.
- **Found:** 71 selected Bazel modules (37 direct), 149 external aliases, and 5,043 rules / 866 packages under `//...`. npm has 48 root direct deps; the website has 1,312 package records. Top wins: dagre-d3-react (one caller, 833,116 bytes of the app bundle), the website globe (45 exclusive snapshots), dead protos (5 Go libraries plus an estimated 4 TS libraries; the Buck subtree has 314 messages), Docker→OCI convergence (7 packages with both rule kinds), and Python Gazelle trimming. The full `deps(//...)` query and the build ran out of disk on the 25 GB VM.
- **Outcome:** Done, survey only.
- **Records:** [surveys/shrink-ext.md](surveys/shrink-ext.md), [workers/shrink-ext/result.md](workers/shrink-ext/result.md).

### shrink-dead
- **Asked:** Find dead targets and code, over-wide BUILD deps, and dead flags.
- **Found:** Decoupling `ioutil` from `server/interfaces` shrinks its standalone closure from 158 to 3 first-party rules. Other wins: the disabled YAML translator (402 lines), three unused Go bundles (49 lines), the pubsub fake plus rbeutil (117 lines), and a healthcheck leaf split (−152 first-party rules simulated). 9 unread flags were found but guard no removable code. Rejected: deleting all 98 libraries outside binaries (most are test fixtures), name-based "experimental" deletions, and an expflag split, which would collide with healthcheck on `server/interfaces`.
- **Outcome:** Done, survey only.
- **Records:** [surveys/shrink-dead.md](surveys/shrink-dead.md), [workers/shrink-dead/result.md](workers/shrink-dead/result.md).

### shrink-cost
- **Asked:** Profile clean-build time and binary size, with a benchmark recipe.
- **Found:** Nothing usable (the partial result is a file listing).
- **Outcome:** Dropped. The user said build time and binary size are not a goal.
- **Records:** [tasks/shrink-cost.md](tasks/shrink-cost.md), [archive/shrink-cost-partial.md](archive/shrink-cost-partial.md).

*Go dependency swaps (wave 1)*

### remote-picker-promptui
- **Asked:** Replace survey with the existing promptui in the `cli/remotebazel` picker (−1 direct module).
- **Found:** The premise held: survey, go-shellquote and mgutz/ansi left the library graph, which shrank from 5,521 to 5,472 external labels (−49). Integration tests couldn't run on the 2-CPU VM because they need 3 CPUs.
- **Outcome:** **Dropped.** The review reproduced two UX regressions that promptui's defaults can't preserve: Ctrl-U/Ctrl-W no longer clear the filter, and Tab-to-advance plus wrap-around arrow navigation are gone. Any retry has to reimplement those keybindings, not just swap the library.
- **Records:** [archive/remote-picker-promptui-result.md](archive/remote-picker-promptui-result.md), [workers/remote-picker-promptui-rev/review.md](workers/remote-picker-promptui-rev/review.md).

### selfauth-one-jwt
- **Asked:** Replace lestrrat-go/jwx in selfauth with the existing golang-jwt/v4 plus an explicit RSA JWKS.
- **Found:** The JWKS is byte-identical to a golden captured from jwx v1.2.29, and the absence of `kid` is preserved. Selfauth's graph drops from 105 to 99 external repos (−6: jwx plus 5 lestrrat helpers) and from 4,835 to 4,692 labels (−143). `goccy/go-json` has no first-party users left either.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/35.
- **Records:** [workers/selfauth-one-jwt/result.md](workers/selfauth-one-jwt/result.md), [workers/selfauth-one-jwt-rev/review.md](workers/selfauth-one-jwt-rev/review.md).

### sendgrid-http
- **Asked:** Replace both SendGrid SDKs with a direct HTTP/JSON POST.
- **Found:** Email was the only user. The new code keeps one personalization per recipient (privacy), context cancellation, and Unavailable status on errors. External BUILD labels go from 4 to 1, and 2 modules have no first-party users left.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/18.
- **Records:** [workers/sendgrid-http/result.md](workers/sendgrid-http/result.md), [workers/sendgrid-http-rev/review.md](workers/sendgrid-http-rev/review.md).

### cpuset-sysfs-stdlib
- **Asked:** Replace cAdvisor's CPU topology in cpuset with sysfs reads, and x/exp/slices with stdlib.
- **Found:** NUMA membership is read from `node*/cpulist`, falling back to the physical package. External dependency entries go from 8 to 4 and cAdvisor labels from 2 to 0. The re-audit against cAdvisor v0.56.2 also noted that CPU ordering may differ (cAdvisor's API never promised an order).
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/42.
- **Review:** P1 regression caught: the fallback counted offline CPUs (cAdvisor checks `/sys/devices/system/cpu/online`), which would inflate the lease pool. Fixed with fixtures for online=0 and a missing online file.
- **Records:** [workers/cpuset-sysfs-stdlib/result.md](workers/cpuset-sysfs-stdlib/result.md), [archive/cpuset-sysfs-stdlib-review3.md](archive/cpuset-sysfs-stdlib-review3.md), [workers/cpuset-sysfs-stdlib-rev/review.md](workers/cpuset-sysfs-stdlib-rev/review.md).

### xcode-xml-plist
- **Asked:** Replace groob/plist with a small `encoding/xml` reader for Xcode's version.plist.
- **Found:** The parser handles reordered and unknown keys and is now testable on Linux. Bazel's Darwin cross-build passed, but the direct consumer builds were never verified because the disk was exhausted.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/29.
- **Records:** [workers/xcode-xml-plist/result.md](workers/xcode-xml-plist/result.md), [workers/xcode-xml-plist-rev/review.md](workers/xcode-xml-plist-rev/review.md).

### test-fixtures-no-faker
- **Asked:** Replace faker with explicit fixtures in two tests.
- **Found:** Faker edges went from 2 to 0. A reflection check fails if a newly added proto field is left unpopulated, so the fixtures can't silently go stale.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/20.
- **Records:** [workers/test-fixtures-no-faker/result.md](workers/test-fixtures-no-faker/result.md).

### yaml-v3-callers
- **Asked:** Migrate 7 structured yaml.v2 callers to yaml.v3.
- **Found:** v3 changes the buildbuddy.yaml parse contract. Duplicate keys go from accepted to an error with no option to turn it off. `timeout: 1000` goes from 1 µs to an error. `cpu: on` changes from bool to string, and a bare date becomes `time.Time`. Imports stayed at 8 → 8.
- **Outcome:** **Dropped**, because v3 changes parse semantics. Don't retry as an import swap. It would need a validated v2-compatibility layer.
- **Records:** [archive/yaml-v3-callers-result.md](archive/yaml-v3-callers-result.md).

### go-small-swaps
- **Asked:** Small swaps: uber atomic→`sync/atomic`, shellwords→`shlex`, and x/exp/slices→`slices` in tests.
- **Found:** The atomic and slices swaps landed; total external labels went from 25 to 23. **The shellwords swap was skipped.** A 13-case comparison showed `shlex.Split` treats `#` comments and `;` differently, which would change rbeperf's arguments. The comparison test is kept.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/36.
- **Records:** [workers/go-small-swaps/result.md](workers/go-small-swaps/result.md).

### memory-lru
- **Asked:** Point the memory KV store and metrics collector at `server/util/lru` instead of hashicorp/golang-lru.
- **Found:** Both first-party edges are removed, but the standalone closure **grows** from 21 to 31 external repos because the repo LRU imports metrics. The hashicorp module stays transitive via go-immutable-radix.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/38.
- **Records:** [workers/memory-lru/result.md](workers/memory-lru/result.md).

### proc-enum-gopsutil
- **Asked:** Replace go-ps with the already-used gopsutil in commandutil and procstats.
- **Found:** go-ps edges went from 2 to 0 with no new module. godemon still pulls go-ps in transitively.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/40.
- **Records:** [workers/proc-enum-gopsutil/result.md](workers/proc-enum-gopsutil/result.md).

*Dead code and over-wide deps (wave 1)*

### ioutil-leaf
- **Asked:** Drop `ioutil`'s dependency on `server/interfaces` by using local structural interfaces.
- **Found:** Closure labels went from 4,576 to 1,630, and first-party rules (excluding analyzers) from 158 to 3, as the survey predicted. All 31 direct dependents built.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/21.
- **Records:** [workers/ioutil-leaf/result.md](workers/ioutil-leaf/result.md).

### healthcheck-leaf
- **Asked:** Move `Checker`/`CheckerFunc` into a leaf package, aliased from interfaces.
- **Found:** Closure labels went from 4,602 to 3,061 and first-party rules from 198 to 49. 31 targets, including 3 binaries, built after a 96-minute local build.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/39.
- **Records:** [workers/healthcheck-leaf/result.md](workers/healthcheck-leaf/result.md).

### delete-yaml-translator
- **Asked:** Delete the disabled `cli/translate/yaml`.
- **Found:** 2 targets and 405 Go lines removed. yaml.v2 is still used elsewhere.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/30.
- **Records:** [workers/delete-yaml-translator/result.md](workers/delete-yaml-translator/result.md).

### delete-go-bundles
- **Asked:** Delete three unused Go bundle targets and keep the resources.
- **Found:** Rules went from 5,043 to 5,040. Runfiles and tar contents were checked byte for byte: all configs and LICENSE still ship.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/47.
- **Review:** First returned changes-needed because the required binary builds never finished (disk full). The worker crashed on the full disk and was redone with `bb remote` builds.
- **Records:** [workers/delete-go-bundles/result.md](workers/delete-go-bundles/result.md), [archive/delete-go-bundles-review3.md](archive/delete-go-bundles-review3.md).

### delete-dead-pkgs
- **Asked:** Delete the pubsub test fake and the rbeutil helper.
- **Found:** 2 targets and 117 Go lines removed, with zero rdeps. Local runs failed on Docker timeouts, and verification was redone remotely.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/19.
- **Records:** [workers/delete-dead-pkgs/result.md](workers/delete-dead-pkgs/result.md).

*Protos and OCI rules (wave 2)*

### prune-dead-protos
- **Asked:** Delete 5 dead protos and their generated Go/TS libraries.
- **Found:** buck_data, buck_error, buck_host_sharing, the local bytestream and kythe_service are gone. Proto targets went from 289 to 279 and 4,107 lines were removed. **There were no TS rules for these protos,** so the survey's estimated 4 TS deletions didn't apply.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/24.
- **Records:** [workers/prune-dead-protos/result.md](workers/prune-dead-protos/result.md).

### oci-converge
- **Asked:** Drop legacy rules_docker image/push rules in 6 packages that already have OCI equivalents.
- **Found:** Only `tools/probers` was converted. The OCI image needed an explicit `0555` mode to match the legacy file permissions, and its payload and config were verified identical. **The other 5 packages were skipped** because their OCI base (`distroless/cc-debian12`) differs from the legacy base (`cc-debian13`). Docker rules went from 12 to 10, not the 12 removals estimated.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/23. Converging the rest first needs a shared base-image change in `deps/oci.MODULE.bazel`.
- **Records:** [workers/oci-converge/result.md](workers/oci-converge/result.md).

*Frontend (wave 2)*

### small-query-graph
- **Asked:** Replace dagre-d3-react in the invocation query graph with an in-repo SVG renderer.
- **Found:** The bundle shrinks from 26,200,283 to 25,324,603 unminified bytes (−875,680, 3.34%) and by 182,046 gzipped bytes (5.14%). The survey's 6.7% share was out of date.
- **Outcome:** **Stopped by the user** while the final viewport fix was in progress, per the ledger. The worker's last report says the fix was committed as `2dcac7749`, but no review of it is recorded. The work is in `archive/small-query-graph.*`.
- **Review:** Three rounds of changes-needed. Edges were drawn through unrelated nodes, first for skip-layer edges and then for adjacent-column fanout. Then every parent re-render reset the user's zoom and pan.
- **Records:** [archive/small-query-graph-result.md](archive/small-query-graph-result.md), [archive/small-query-graph.patch](archive/small-query-graph.patch), [archive/small-query-graph-review.md](archive/small-query-graph-review.md), [archive/small-query-graph-review3.md](archive/small-query-graph-review3.md), [archive/small-query-graph-review-last.md](archive/small-query-graph-review-last.md).

### one-diff-engine
- **Asked:** Replace diff-match-patch with the already-present `diff` in `app/util/diff.tsx`.
- **Found:** `diff` 8.0.3 has no equivalent of diff-match-patch's semantic cleanup. Both `diffChars` and `diffWordsWithSpace` change the visible highlighting on 2 of 10 fixtures, for example `fastbuild → opt` renders as `[fas]t[build]`.
- **Outcome:** **Dropped**, because the swap changes visible highlighting. A retry would need its own maintained cleanup pass.
- **Records:** [archive/one-diff-engine-result.md](archive/one-diff-engine-result.md).

### uuid-browser-api
- **Asked:** Replace the npm `uuid` package with `crypto.randomUUID()`.
- **Found:** `randomUUID()` needs a secure context. The docs show plain-HTTP deployments (`http://buildbuddy.acme.corp`), where `uuid`'s `getRandomValues` fallback works but the direct call would throw.
- **Outcome:** **Dropped** to keep HTTP deployments working.
- **Records:** [archive/uuid-browser-api-result.md](archive/uuid-browser-api-result.md).

---

## Test speed

The goal was to cut test time, especially for slow integration tests, without losing coverage. Every task had to re-verify its premise and stop if it was wrong, keep identical before/after case lists, and pass repeated runs. Three surveys fed three waves. 22 PRs are open, and five tasks were dropped, four of them because the premise was wrong when measured. Measured wins per the ledger: raft txn 183 → 81 s (#14), remote_bazel longest shard 92 → 23 s (#31), CLI integration 107 → 50 s and stream_run_logs 55 → 19 s (#15), clickhouse execution_search 45 → 8 s (#45), raft ErrorCodes 18 → 0 s (#25), podman 25 → 15 s (#27). Most measured savings were below the survey models (see Lessons). Ledger discrepancy: the Threads line says "37 PRs open from this effort (#11–#47)", but the table shows 22 test-speed PRs. The other 15 numbers in that range belong to build simplification. Still pending per the ledger: rerun tests-hotspots for wave 4, and three tasks held until merges: lru-clock-ticker (#32, #36), commandutil timeout (#40) and build-logs-shared-app (#37).

*Surveys*

### tests-ci-timing
- **Asked:** Get per-target test durations from upstream CI and rank the 40 slowest.
- **Found:** GitHub log downloads need admin/auth, so the timings came from 20 public BuildBuddy CI invocations across 5 master commits, which use different flags than GitHub CI. `remote_bazel_test` shard 4 sat on the critical path in 5 of 5 runs. Unsharded raft metadata (136.42 s), codesearch index (120.43 s, cold) and txn's 200.65 s max shard were flagged. Rejected: cutting the benchmark run length, changing retry policy, blind `t.Parallel`, faking OCI networking, global deadline changes, and OOM-killer sleeps (they run under synctest).
- **Outcome:** Done, survey only.
- **Records:** [surveys/tests-ci-timing.md](surveys/tests-ci-timing.md), [workers/tests-ci-timing/result.md](workers/tests-ci-timing/result.md).

### tests-patterns
- **Asked:** Find sleeps, polls, timeouts and serial cases in tests.
- **Found:** Local spot checks: raft ErrorCodes ~18 s, disk metadata 22.2 → 1.6 s across three repetitions, approxlru 12.402 → 0.394 s across three repetitions. Workflow has 26 polling sites that back off up to 51.2 s. Rejected: OOM-killer sleeps (synctest), large timeout values that aren't actually waited on, and the rate limiter's asserted elapsed time. `randSleepMillis` in RBE integration is actually nanoseconds (missing `time.Millisecond`), so it gives no saving.
- **Outcome:** Done, survey only.
- **Records:** [surveys/tests-patterns.md](surveys/tests-patterns.md), [workers/tests-patterns/result.md](workers/tests-patterns/result.md).

### tests-harness
- **Asked:** Measure integration-harness setup cost and find setup that can be shared.
- **Found:** This is a draft and its measurement section is unfinished. It mapped each harness's setup cost and proposed the wave 3 candidates, marking edits to shared helpers as serialize-only. testbazel already pre-extracts and pre-warms. rules_go assigns top-level tests to shards by index modulo shard count, so package-level sharing doesn't help one-test-per-shard targets.
- **Outcome:** Stopped: disk nearly full from local builds.
- **Records:** [surveys/tests-harness-draft.md](surveys/tests-harness-draft.md).

### tests-hotspots
- **Asked:** Measure per-case hotspots in the ~25 slowest targets via `bb remote` before proposing anything. The spec notes that "roughly a third of the premises were wrong once measured."
- **Found:** Nothing, because it was stopped before producing results.
- **Outcome:** Stopped by the user. The ledger says to rerun it to seed wave 4.
- **Records:** [tasks/tests-hotspots.md](tasks/tests-hotspots.md).

*Wave 1*

### controlled-action-merging
- **Asked:** Replace `sleep 60` in `TestActionMerging_LongTask` with a controlled command, to save the ~60 s that tests-ci-timing modelled from a 72.61 s XML case.
- **Found:** Measured remotely, the test takes **11.02 s**. Cleanup kills the command about 10 s after shutdown starts; the test never waits the full 60 s.
- **Outcome:** **Dropped: premise wrong** (11 s, not 60 s). The draft was reverted.
- **Records:** [archive/controlled-action-merging-result.md](archive/controlled-action-merging-result.md).

### txn-shardable-fault-matrix
- **Asked:** Turn the 12 raft txn split-retry subtests into top-level tests so the existing 8 shards can spread them.
- **Found:** The matrix alone took 182.61 s on one shard. Target wall time went from 182.9 to 80.9 s (−102.0 s, 55.8%; the model said ~129 s). Total work was unchanged (363.40 vs 364.92 s).
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/14.
- **Records:** [workers/txn-shardable-fault-matrix/result.md](workers/txn-shardable-fault-matrix/result.md).

### oci-shardable-network-matrix
- **Asked:** Turn the six ociruntime networking subtests into top-level tests.
- **Found:** The longest shard's networking time went from 28.86 to 14.12 s. The 72 s model did not reproduce. 30 of 30 repeated executions passed. The first attempt ran zero tests because it didn't select the Docker suite.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/11.
- **Records:** [workers/oci-shardable-network-matrix/result.md](workers/oci-shardable-network-matrix/result.md).

### hosted-bazel-shards-and-polls
- **Asked:** Split remote_bazel's critical shard (the task said it held EmbeddedBinariesFromApp and BuildRemotelyRunLocally) and cap polling at 100 ms with an elapsed-time deadline. The task estimated up to ~13 s saved.
- **Found:** The real bottleneck was `TestBuildRemotelyRunLocally_Runfiles`: three serial subtests of ~30 s each, mostly nested Bazel startup. Splitting them and going to 13 shards cut the median longest shard from 92.251 to 22.524 s.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/31.
- **Review:** Round 1 found the worker had deliberately skipped the polling cap. Round 2 found the RPCs still used `context.Background()`, so a stalled RPC could overrun the 102.35 s budget. Round 3 approved.
- **Records:** [workers/hosted-bazel-shards-and-polls/result.md](workers/hosted-bazel-shards-and-polls/result.md), [archive/hosted-bazel-shards-and-polls-review.md](archive/hosted-bazel-shards-and-polls-review.md), [archive/hosted-bazel-shards-and-polls-review2.md](archive/hosted-bazel-shards-and-polls-review2.md).

### raft-shards
- **Asked:** Shard raft metadata, client and sender (×4), and raise store from 8 to 16 shards.
- **Found:** XML critical path: metadata 113.442 → 45.347 s, sender 31.493 → 9.214 s, store 140.617 → 122.804 s. Client showed no XML gain (22.963 → 23.510 s), though a paired run including overhead went from 30.566 to 20.057 s. Store's paired run with overhead went from 143.719 to 128.116 s (10.9%). The modelled ~80 s for store and ~40 s for client were not reproduced. The metadata baseline was flaky beforehand (`TestDistributedRanges`).
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/16.
- **Records:** [workers/raft-shards/result.md](workers/raft-shards/result.md).

### misc-shards
- **Asked:** Shard codesearch index, CLI integration (4 → 8) and stream_run_logs.
- **Found:** CLI: 142.6 → 64.2 s and 107.5 → 50.0 s in two pairs. stream_run_logs (1 → 5 shards): 32.6 → 21.9 s and 55.3 → 19.3 s. **The codesearch index was left unsharded:** it measured 0.6–0.7 s, not the survey's 120 s.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/15.
- **Records:** [workers/misc-shards/result.md](workers/misc-shards/result.md).

### pebble-completed-sweep
- **Asked:** Replace two 3 s sleeps in pebble cache tests with a wait for a completed janitor sweep.
- **Found:** Only the higher-threshold case was kept: 3.02 → 1.01 s, about 2 s saved, against a ~6 s model. The minimum-age test was restored byte for byte, because its janitor blocks waiting for eligible samples, so waiting for a completed sweep can't work there.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/32.
- **Review:** Round 1 found a schedule where the new wait could hang. Round 2 found coverage weakened: the min-age test waited only for a sampling batch, so an age-filter regression could pass. Round 3 approved after the revert.
- **Records:** [workers/pebble-completed-sweep/result.md](workers/pebble-completed-sweep/result.md), [archive/pebble-completed-sweep-review.md](archive/pebble-completed-sweep-review.md), [archive/pebble-completed-sweep-review2.md](archive/pebble-completed-sweep-review2.md).

### podman-deadline-handshake
- **Asked:** Shorten the Podman Run/Exec timeout deadlines behind a handshake, keeping the stats assertions.
- **Found:** `TestExec_Timeout` calls `Run`, not `Exec`; it duplicates the Run test exactly. Neither test asserts stats. Each takes ~25 s (25.25 and 25.45 s), of which the 5 s deadline is only part; a failed container shutdown accounts for about 15 s.
- **Outcome:** **Dropped: premise wrong** (no stats asserts; Exec calls Run). This led to podman-duplicate-timeout.
- **Records:** [archive/podman-deadline-handshake-result.md](archive/podman-deadline-handshake-result.md).

### workflow-capped-polling
- **Asked:** Cap the workflow integration test's polling backoff (which grew to 51.2 s) at 100 ms, keeping the timeout budget.
- **Found:** Median slowest shard went from 63.23 to 44.41 s. Summed testcase time over 5 runs went from 1068.58 to 786.03 s.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/17.
- **Records:** [workers/workflow-capped-polling/result.md](workers/workflow-capped-polling/result.md).

### compact-parallel-repetitions
- **Asked:** Run the CLI compact test's 12 independent executions in parallel.
- **Found:** 2.2 → 1.1 s. The modelled ~9 s saving did not reproduce; the change was kept anyway.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/13.
- **Records:** [workers/compact-parallel-repetitions/result.md](workers/compact-parallel-repetitions/result.md).

### singleflight-follower-barrier
- **Asked:** Replace two 2 s sleeps in the dsingleflight tests with a barrier.
- **Found:** The barrier waits until Redis `CLIENT LIST` shows all 20 readers blocked. Combined time went from 4.112 to 0.062 s.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/22.
- **Review:** Caught a real bug. go-redis's default pool is 10×GOMAXPROCS, so with GOMAXPROCS=1 only 10 readers can block and the barrier times out. Fixed with `PoolSize = 21` and verified 5/5 at GOMAXPROCS=1. Also flagged result.md for wrongly claiming no BUILD changes.
- **Records:** [workers/singleflight-follower-barrier/result.md](workers/singleflight-follower-barrier/result.md), [workers/singleflight-follower-barrier-rev/feedback.md](workers/singleflight-follower-barrier-rev/feedback.md).

### notification-subscribe-readiness
- **Asked:** Replace a 3 s sleep with a Redis subscription readiness check.
- **Found:** Polls `PUBSUB NUMSUB` on `serverNotifications/app` until there are 2 subscribers. 3.028 → 0.032 s.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/12.
- **Records:** [workers/notification-subscribe-readiness/result.md](workers/notification-subscribe-readiness/result.md).

*Wave 2*

### raft-error-time
- **Asked:** Run the raft client ErrorCodes table under `synctest`.
- **Found:** 18.00 → 0.00 s. All 12 cases and the 3 s deadlines are unchanged.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/25.
- **Records:** [workers/raft-error-time/result.md](workers/raft-error-time/result.md).

### disk-metadata-clock
- **Asked:** Replace disk_cache timestamp sleeps with a readiness check.
- **Found:** This checkout had seven 1 s sleeps, not the survey's 22.2 s. `TestMetadata` went from 7.09 to 0.186 s. The wait polls until the filesystem's atime and mtime actually advance; a fixed 1 ms wait had failed in the survey.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/26.
- **Records:** [workers/disk-metadata-clock/result.md](workers/disk-metadata-clock/result.md).

### podman-duplicate-timeout
- **Asked:** Deal with the duplicate `TestExec_Timeout` (preferably by converting it, not deleting it).
- **Found:** The two bodies were byte-identical (1,577 bytes each). It was converted into a real Exec timeout test, which no other test covered. Median went from 25.20 to 15.30 s (−9.90 s, versus a 5 s estimate).
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/27.
- **Records:** [workers/podman-duplicate-timeout/result.md](workers/podman-duplicate-timeout/result.md).

### executor-download-fixture
- **Asked:** Shrink a 128 MiB executor test fixture and gate a 5-minute fake run.
- **Found:** The fixture is now `BatchReadLimitBytes + 1` (2,097,153 bytes), and the test asserts that ByteStream is still used. The download-timeout median went from 3.47 to 0.42 s. The 5-minute sleep was in a background goroutine, so removing it saves no wall time.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/28.
- **Records:** [workers/executor-download-fixture/result.md](workers/executor-download-fixture/result.md).

### rbe-command-barriers
- **Asked:** Replace `sleep 5` in two RE integration tests with readiness and release gates.
- **Found:** `TestRedisRestart` went from 14.95 to 8.25 s; merged cancellation from 5.78 to 0.77 s. The full suite of 55 top-level plus 9 nested cases passed.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/33.
- **Records:** [workers/rbe-command-barriers/result.md](workers/rbe-command-barriers/result.md).

### testserver-readiness
- **Asked:** Reduce the shared testserver readiness poll from 500 ms to 25–50 ms.
- **Found:** Changed to 50 ms. remote_cache: 18.702 → 12.164 s; remote_asset: 13.684 → 10.552 s; BEP: 14.076 → 12.076 s; build_logs: no gain (3.956 → 4.072 s). In-process rbetest servers bypass this helper.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/34.
- **Records:** [workers/testserver-readiness/result.md](workers/testserver-readiness/result.md).

### rbe-registration-poll
- **Asked:** Reduce the rbetest executor-registration poll from 100 ms to 5–10 ms.
- **Found:** 136 of 139 registration calls never slept. Total wait went from 0.817 to 0.446 s, not the modelled 12.51 s. Kept as a harmless one-liner (10 ms).
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/41.
- **Records:** [workers/rbe-registration-poll/result.md](workers/rbe-registration-poll/result.md).

### small-waits
- **Asked:** Six small waits: workflow service (a), container pull (b), hit tracker (c), webdriver (d), build_logs (e) and tasksize (f).
- **Found:** Kept (a), (c) and (e): hit tracker 2.50 → 0.62 s, workflow service 10.23 → 6.47 s, build_logs 6.44 → 2.59 s (summed 19.17 → 9.68 s). **(b) was reverted** after review. **(d) was left unchanged:** no measurable saving (20.50 vs 20.60 s). **(f) was left unchanged:** it needs an external TensorFlow serving instance.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/37.
- **Review:** P2 on (b). The container barrier counted signals emitted before the production mutex, so the test might no longer catch a serialization regression. The last recorded review is this changes-needed one; the worker then removed (b) entirely.
- **Records:** [workers/small-waits/result.md](workers/small-waits/result.md), [workers/small-waits-rev/review.md](workers/small-waits-rev/review.md).

*Wave 3 (harness)*

### nested-bazel-lifetime
- **Asked:** Keep testbazel's nested JVM alive per workspace across gaps longer than 5 s (`--max_idle_secs=5`).
- **Found:** remote_cache (40 executions) and webdriver invocation (15) each started exactly one JVM per case. There were no restarts to avoid.
- **Outcome:** **Dropped: premise wrong** (already one JVM per workspace).
- **Records:** [archive/nested-bazel-lifetime-result.md](archive/nested-bazel-lifetime-result.md).

### stream-logs-shared-app
- **Asked:** Share one app fixture across the stream_run_logs cases.
- **Found:** After #15's 5 shards, each shard runs exactly one case, so sharing removes zero app startups.
- **Outcome:** **Dropped: premise wrong** (one case per shard after #15).
- **Records:** [archive/stream-logs-shared-app-result.md](archive/stream-logs-shared-app-result.md).

### prober-preextracted-install
- **Asked:** Give bazelrbe_prober pre-extracted Bazel 6.5.0 and 7.7.1 installs.
- **Found:** "Extracting Bazel installation" messages went from 10 to 0. Bazel6 12.220 → 9.128 s, Bazel7 13.502 → 11.884 s, sum 25.722 → 21.012 s (18.3%). The first run failed because `t.TempDir` cleanup couldn't remove the read-only copies; fixed with chmod.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/43.
- **Records:** [workers/prober-preextracted-install/result.md](workers/prober-preextracted-install/result.md).

### rbe-borrow-owned-db
- **Asked:** Let rbetest apps borrow the harness DB so they skip their own migration, which is discarded anyway.
- **Found:** 67 migrations avoided per suite, ~18 ms per app (projected ~1.21 s total). Suite timing was noisy (255.484 s before, then 204.466 and 264.891 s after). One run failed `TestSimpleCommand_Timeout_StdoutStderrStillVisible` with NotFound; 20 of 20 focused reruns passed.
- **Outcome:** **Dropped:** ~18 ms per app and no suite speedup did not justify the shared testenv risk and the timeout flake. It also overlapped `rbetest.go` with rbe-registration-poll.
- **Records:** [archive/rbe-borrow-owned-db-result.md](archive/rbe-borrow-owned-db-result.md).

### ci-runner-safe-app-hoist
- **Asked:** Start the app once per parent in two ci_runner table tests.
- **Found:** Four startups become two. `BazelWithOptions`: 26.10 → 21.82 s; `RespectsCommitSha`: 46.01 → 43.83 s; combined 72.12 → 65.65 s (~9%).
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/44.
- **Records:** [workers/ci-runner-safe-app-hoist/result.md](workers/ci-runner-safe-app-hoist/result.md).

### clickhouse-process-db-namespaces
- **Asked:** One ClickHouse process per test binary, with a fresh database per test, for execution_search.
- **Found:** Processes per binary went from 8 to 1, and the real migrations still run (40 per 5 runs). Test-action time went from 45.2 to 8.4 s, largely from skipping per-test shutdown waits.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/45.
- **Records:** [workers/clickhouse-process-db-namespaces/result.md](workers/clickhouse-process-db-namespaces/result.md).

### postgres-direct-reset
- **Asked:** Reset Postgres test databases with SQL instead of two `docker exec` subprocesses.
- **Found:** Each run does 134 resets, so 268 subprocesses are removed per run. Three targets' resets got 49–71% faster. The usage target swung from +180% to −64.3% between runs because of remote variance. The aggregate went from 456.56 to 379.37 s.
- **Outcome:** **Stopped by the user** while in review (per the ledger). The recorded review approved it. The patch is saved and no PR was opened.
- **Records:** [archive/postgres-direct-reset-result.md](archive/postgres-direct-reset-result.md), [archive/postgres-direct-reset.patch](archive/postgres-direct-reset.patch), [archive/postgres-direct-reset-review.md](archive/postgres-direct-reset-review.md).

### explain-minimal-fixture
- **Asked:** Drop workspace setup from the explain tests, which only read fixture logs.
- **Found:** With `--old`/`--new` set, explain skips the workspace history lookup. Mean time went from 2.738 to 0.707 s.
- **Outcome:** PR open, https://github.com/dan-stowell/buildbuddy/pull/46.
- **Records:** [workers/explain-minimal-fixture/result.md](workers/explain-minimal-fixture/result.md).

---

## OCI fetcher split plan

The owner's four upstream drafts move OCI fetching so the cache proxy talks to registries directly. They are too big to land as they are, so one analysis worker planned a series of small PRs. Nothing was implemented, built or tested. The open question for the user (ledger, 2026-10-13) is whether to start re-cutting the proxy track.

### ocifetch-split-plan
- **Asked:** Split upstream drafts #13762, #13763, #13764 and #13772 into a landable series of PRs under ~200 lines each, behaviour-neutral first.
- **Found:** Actual diffs: #13762 +17/−17; #13763 +761/−68, which includes #13762; #13764 +94/−40; #13772 +547/−8. #13772 does **not** need #13763: it uses the existing `ocifetcher.NewServer`. The plan has 16 PRs in two independent tracks: proxy (items 1–9, then rollout) and executor-local (10–15, then rollout). Blockers before rollout:
  - #13772's `FetchBlob` sends can outlive the RPC (a stream-lifetime race).
  - The drafts don't test private-registry auth or tenant isolation.
  - Both drafts initialize eagerly, and #13763 has no rollback flag.
  - The 401/403/429 fallback can double work or amplify throttling.
- **Outcome:** Done, plan only. The plan: keep #13762, re-cut #13764 and #13772 first (the proxy track), re-cut #13763 separately.
- **Records:** [plans/ocifetch-split-plan.md](plans/ocifetch-split-plan.md), [tasks/ocifetch-split-plan.md](tasks/ocifetch-split-plan.md).

---

## Lessons

- **Measure the premise before building.** Five tasks found their premise wrong once measured. Four were dropped: controlled-action-merging (11 s, not 60), podman-deadline-handshake (Exec calls Run, no stats asserts), nested-bazel-lifetime (already one JVM per workspace) and stream-logs-shared-app (one case per shard). The fifth, misc-shards, skipped the codesearch index (0.6–0.7 s, not 120 s). The tests-hotspots spec puts it at "roughly a third of the premises were wrong once measured." The explicit stop-and-explain rule in each spec is what turned these into cheap negative results.
- **Survey models overstate savings; record the measured number.** txn saved 102 s against ~129 modelled, store 15.6 s against ~80, OCI networking ~14.7 s against 72, pebble ~2 s against 6, compact 1.1 s against 9, and registration polling 0.37 s against 12.51 s. A few beat their estimates (hosted-bazel saved 69.7 s against ~13, podman 9.9 s against 5), usually because the real bottleneck wasn't the modelled one.
- **Don't trust cached or historical CI timings as current cost.** tests-ci-timing warned that cached durations are inherited. controlled-action-merging's 72.61 s XML case did not reflect what the test does today.
- **Behaviour-preservation stop rules kill bad dependency swaps early.** yaml.v3 (parse semantics), the `diff` library (highlighting), `crypto.randomUUID` (HTTP deployments), shellwords→shlex (`#` and `;` handling), and 5 of 6 OCI packages (different base image) all stopped with written evidence instead of a quiet regression.
- **The adversarial review earns its cost.** It caught a P1 offline-CPU regression (cpuset), a GOMAXPROCS=1 deadlock (singleflight), an unbounded RPC (hosted-bazel), two coverage weakenings disguised as speedups (pebble min-age, small-waits container barrier), picker UX regressions (promptui), and three graph rendering bugs. It also caught a worker silently skipping half its task (hosted-bazel polling).
- **When a sleep is replaced, prove the test still fails on the regression it guards.** Both coverage problems the reviews found came from waits that signalled before the behaviour under test had a chance to go wrong.
- **Don't run local Bazel on worker VMs.** Several workers filled the 25 GB disk (proc-enum, oci-converge, delete-go-bundles, xcode, memory-lru, tests-harness). Repo-wide `rdeps(//...)` queries failed on Docker image repo fetch timeouts, and the 2-CPU VM can't schedule remote_bazel integration tests (they need 3 CPUs). `bb remote` with scoped queries worked.
- **Single remote runs are noisy; use repeated, paired runs with the same method.** The postgres usage target swung from +180% to −64% with unchanged code. rbe-borrow's suite time went down, then up. Raft client timing varied by runner. Whole-invocation elapsed time mixes in build and cache effects.
- **Removing a first-party import isn't removing a dependency.** Module deletions are batched into one dep-prune PR on the shared `go.mod`. Some swaps leave the module transitive (hashicorp LRU, go-ps, yaml.v2). memory-lru made its standalone closure larger (21 → 31 external repos).
- **Serialize edits to shared harness files.** The harness survey marked shared helpers as serialize-only, and rbe-borrow-owned-db and rbe-registration-poll both edited `rbetest.go`. Keep the worker's result.md out of the branch: almost every PR-prep feedback had to ask for it, and one report (singleflight) misdescribed the diff.
