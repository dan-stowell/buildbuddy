# Ledger

| id | task file | state | VM(s) | PR | notes |
|----|-----------|-------|-------|----|-------|
| ext4-stream | tasks/ext4-stream.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/8 | tar stream -> ext4 directly (mke2fs -d tarball, tar2ext4) |
| ext4-mkfs | tasks/ext4-mkfs.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/6 | mke2fs flags, sparseness, journal, sizing, tmpfs |
| ext4-audit | tasks/ext4-audit.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/7 | layer fetch / filecache / post-conversion copies |
| gaps-mkfs | tasks/gaps-mkfs.md | done: pushed to #6, reaped | - | (updates #6) | test gaps on branch t-ext4-mkfs; finish by `git push origin HEAD:t-ext4-mkfs` on VM, not bin/pr |
| gaps-audit | tasks/gaps-audit.md | done: pushed to #7, reaped | - | (updates #7) | test gaps on branch t-ext4-audit; push HEAD:t-ext4-audit |
| gaps-stream | tasks/gaps-stream.md | done: pushed to #8, reaped | - | (updates #8) | test gaps on branch t-ext4-stream; push HEAD:t-ext4-stream |
| proxy-splitblob | tasks/proxy-splitblob.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/10 | verify+fix proxy #1/#3 (SplitBlob before local hit; manifest on write) |
| proxy-partial-read | tasks/proxy-partial-read.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/9 | verify+fix proxy #2 (partial read misses) |
| metrics-ext4 | tasks/metrics-ext4.md | stopped (user refocused on build shrink), reaped | - | | work saved in archive/metrics-ext4.patch + archive/metrics-ext4-result.md; observe-only metrics for ext4 conversion decisions |
| metrics-proxy | tasks/metrics-proxy.md | stopped (user refocused on build shrink), reaped | - | | work saved in archive/metrics-proxy.patch + archive/metrics-proxy-result.md; observe-only metrics for proxy↔app decisions |
| handoff-design | tasks/handoff-design.md | stopped (user refocused on build shrink), reaped | - | (none: design doc) | work saved in archive/handoff-design.patch + archive/handoff-design-result.md; research: eliminate dropped hinted handoffs; deliverable is result.md |
| rewind-design | tasks/rewind-design.md | stopped (user refocused on build shrink), reaped | - | (none: design doc) | work saved in archive/rewind-design.patch + archive/rewind-design-result.md; research: Bazel action rewinding vs cache presence guarantees. NOTE: topology is only client->proxy->app; tell worker on iterate if doc assumes chains |
| shrink-go | tasks/shrink-go.md | done, reaped | - | (none: survey) | report in surveys/shrink-go.md; phase 0: Go module deps removable/replaceable |
| shrink-ext | tasks/shrink-ext.md | done, reaped | - | (none: survey) | report in surveys/shrink-ext.md; phase 0: Bazel externals, rules, toolchains, npm, protos |
| shrink-dead | tasks/shrink-dead.md | done, reaped | - | (none: survey) | report in surveys/shrink-dead.md; phase 0: dead targets/code, over-wide BUILD deps |
| shrink-cost | tasks/shrink-cost.md | dropped (user: not a goal), reaped | - | (none: survey) | phase 0: build time + binary size profile, benchmark recipe |
| remote-picker-promptui | tasks/remote-picker-promptui.md | dropped: review found picker regressions (Ctrl-U filter clear, Tab/wrap nav); reaped | - | | wave 1: survey->promptui (cli/remotebazel); -1 direct mod |
| selfauth-one-jwt | tasks/selfauth-one-jwt.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/35 | wave 1: jwx->golang-jwt in selfauth; -1 direct, up to -6 indirect |
| sendgrid-http | tasks/sendgrid-http.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/18 | wave 1: SendGrid SDKs->HTTP; -2 direct |
| cpuset-sysfs-stdlib | tasks/cpuset-sysfs-stdlib.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/42 | wave 1: cAdvisor->sysfs in cpuset; -1 direct |
| xcode-xml-plist | tasks/xcode-xml-plist.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/29 | wave 1: plist lib->encoding/xml; -1 direct |
| test-fixtures-no-faker | tasks/test-fixtures-no-faker.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/20 | wave 1: faker->explicit fixtures in 2 tests; -1 direct |
| yaml-v3-callers | tasks/yaml-v3-callers.md | dropped: v3 changes parse semantics (dup keys, durations, resources); reaped | - | | result in archive/yaml-v3-callers-result.md; wave 1: 7 yaml.v2 callers->v3 |
| go-small-swaps | tasks/go-small-swaps.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/36 | wave 1: uber atomic, shellwords, x/exp/slices edges |
| memory-lru | tasks/memory-lru.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/38 | wave 1: memory backends->server/util/lru |
| proc-enum-gopsutil | tasks/proc-enum-gopsutil.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/40 | wave 1: go-ps->gopsutil |
| ioutil-leaf | tasks/ioutil-leaf.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/21 | wave 1: ioutil drops interfaces dep (closure ~158->3 rules) |
| delete-yaml-translator | tasks/delete-yaml-translator.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/30 | wave 1: delete disabled cli/translate/yaml |
| delete-go-bundles | tasks/delete-go-bundles.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/47 | wave 1: delete 3 unused Go bundle targets |
| delete-dead-pkgs | tasks/delete-dead-pkgs.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/19 | wave 1: delete pubsub fake + rbeutil |
| healthcheck-leaf | tasks/healthcheck-leaf.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/39 | wave 1: Checker types to leaf pkg (closure -152 rules) |
| prune-dead-protos | tasks/prune-dead-protos.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/24 | wave 2: delete 5 dead protos + generated Go/TS libs |
| oci-converge | tasks/oci-converge.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/23 | wave 2: drop legacy rules_docker image/push in 6 pkgs (not rbeperf) |
| small-query-graph | tasks/small-query-graph.md | stopped by user (final viewport fix in progress); work in archive/small-query-graph.*; reaped | - | | wave 2: dagre-d3-react -> in-repo SVG graph (-833KB bundle) |
| one-diff-engine | tasks/one-diff-engine.md | dropped: diff lib changes visible highlighting on 2/10 fixtures; reaped | - | | result in archive/one-diff-engine-result.md; wave 2: diff-match-patch -> diff in app/util/diff.tsx |
| uuid-browser-api | tasks/uuid-browser-api.md | dropped: crypto.randomUUID needs secure context, uuid supports HTTP deploys; reaped | - | | result in archive/uuid-browser-api-result.md; wave 2: uuid -> crypto.randomUUID |
| tests-ci-timing | tasks/tests-ci-timing.md | done, reaped | - | (none: survey) | report in surveys/tests-ci-timing.md; test speed survey: per-target test durations from upstream CI, top 40 slow |
| tests-harness | tasks/tests-harness.md | stopped (disk ~full, local builds); draft in surveys/tests-harness-draft.md; reaped | - | (none: survey) | test speed survey: integration harness setup cost, shareable setup |
| tests-patterns | tasks/tests-patterns.md | done, reaped | - | (none: survey) | report in surveys/tests-patterns.md; test speed survey: sleeps/polls/timeouts/serial cases in tests |
| controlled-action-merging | tasks/controlled-action-merging.md | dropped: premise wrong (test is 11s, not 60s); reaped | - | | test speed: drop sleep 60 in TestActionMerging_LongTask (~60s) |
| txn-shardable-fault-matrix | tasks/txn-shardable-fault-matrix.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/14 | test speed: raft txn split-retry cases top-level (200s shard) |
| oci-shardable-network-matrix | tasks/oci-shardable-network-matrix.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/11 | test speed: ociruntime network cases top-level |
| hosted-bazel-shards-and-polls | tasks/hosted-bazel-shards-and-polls.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/31 | test speed: remote_bazel critical shard split + capped polls |
| raft-shards | tasks/raft-shards.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/16 | test speed: shard raft metadata/client/sender/store |
| misc-shards | tasks/misc-shards.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/15 | test speed: shard codesearch index, cli, stream_run_logs |
| pebble-completed-sweep | tasks/pebble-completed-sweep.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/32 | test speed: pebble janitor sweep hook vs 3s sleeps |
| podman-deadline-handshake | tasks/podman-deadline-handshake.md | dropped: premise wrong (no stats asserts, Exec calls Run); reaped | - | | test speed: podman timeout tests shorter deadline |
| workflow-capped-polling | tasks/workflow-capped-polling.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/17 | test speed: workflow test capped poll backoff |
| compact-parallel-repetitions | tasks/compact-parallel-repetitions.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/13 | test speed: parallel cli compact repetitions |
| singleflight-follower-barrier | tasks/singleflight-follower-barrier.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/22 | test speed: dsingleflight barrier vs 2s sleeps |
| notification-subscribe-readiness | tasks/notification-subscribe-readiness.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/12 | test speed: NUMSUB wait vs 3s sleep |
| raft-error-time | tasks/raft-error-time.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/25 | test speed wave 2: synctest for raft client ErrorCodes (~18s) |
| disk-metadata-clock | tasks/disk-metadata-clock.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/26 | test speed wave 2: disk_cache timestamp readiness (~7s) |
| podman-duplicate-timeout | tasks/podman-duplicate-timeout.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/27 | test speed wave 2: drop/convert duplicate TestExec_Timeout |
| executor-download-fixture | tasks/executor-download-fixture.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/28 | test speed wave 2: 128MiB->3MiB fixture + gate |
| rbe-command-barriers | tasks/rbe-command-barriers.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/33 | test speed wave 2: gates vs sleep 5 in RE integration |
| testserver-readiness | tasks/testserver-readiness.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/34 | test speed wave 2: SHARED: testserver poll 500ms->25-50ms |
| rbe-registration-poll | tasks/rbe-registration-poll.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/41 | test speed wave 2: SHARED: rbetest registration poll 100->5-10ms |
| small-waits | tasks/small-waits.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/37 | test speed wave 2: 6 small waits (workflow svc, container pull, hit tracker, webdriver, build_logs, tasksize) |
| nested-bazel-lifetime | tasks/nested-bazel-lifetime.md | dropped: premise wrong (1 JVM per workspace already); reaped | - | | test speed wave 3 (harness): SHARED testbazel: keep nested JVM alive per workspace |
| stream-logs-shared-app | tasks/stream-logs-shared-app.md | dropped: premise wrong (1 case per shard after #15); reaped | - | | test speed wave 3 (harness): one app fixture across cases |
| prober-preextracted-install | tasks/prober-preextracted-install.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/43 | test speed wave 3 (harness): pre-extracted bazel installs |
| rbe-borrow-owned-db | tasks/rbe-borrow-owned-db.md | dropped: ~18ms/app, no suite speedup, shared testenv risk, one timeout flake; reaped | - | | test speed wave 3 (harness): SHARED testenv/rbetest: skip discarded DB migration (rbetest.go also in rbe-registration-poll) |
| ci-runner-safe-app-hoist | tasks/ci-runner-safe-app-hoist.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/44 | test speed wave 3 (harness): hoist app in 2 table tests |
| clickhouse-process-db-namespaces | tasks/clickhouse-process-db-namespaces.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/45 | test speed wave 3 (harness): SHARED testclickhouse: per-process server, per-test DB |
| postgres-direct-reset | tasks/postgres-direct-reset.md | stopped by user (was in review); work in archive/postgres-direct-reset.*; reaped | - | | test speed wave 3 (harness): SHARED testpostgres: SQL reset vs docker exec |
| explain-minimal-fixture | tasks/explain-minimal-fixture.md | PR open, reaped | - | https://github.com/dan-stowell/buildbuddy/pull/46 | test speed wave 3 (harness): no workspace for fixture-log cases |
| tests-hotspots | tasks/tests-hotspots.md | stopped by user before results; reaped | - | (none: survey) | measured per-case hotspots in ~25 slowest targets via bb remote; feeds wave 4 |
| ocifetch-split-plan | tasks/ocifetch-split-plan.md | done, reaped | - | (none: plan) | analysis: split upstream drafts #13762/#13763/#13764/#13772 (cache proxy OCI fetcher -> registries) into a landable series |

## Threads
- **OCI fetcher split (2026-10-13)**: plan in plans/ocifetch-split-plan.md: 16 PRs in two tracks. Proxy track (#13762 as-is, re-cut #13764 and #13772) is independent of the executor-local track (re-cut #13763). Blockers: FetchBlob stream-lifetime race, private-registry auth tests, lazy/gated init. Q for user: start re-cutting the proxy track?
- **Paused otherwise (2026-10-13)**: all other t-* VMs reaped. Unfinished work saved in archive/ (postgres-direct-reset, small-query-graph, tests-hotspots had nothing yet).
- **Test speed**: 37 PRs open from this effort (#11-#47). Biggest: raft txn 183->81s (#14), remote_bazel longest shard 92->23s (#31), CLI integration 107->50s (#15), stream_run_logs 55->19s (#15), clickhouse/execution_search 45->8s (#45), raft ErrorCodes 18->0s (#25), podman 25->15s (#27). To resume: rerun tests-hotspots survey for wave 4. Held until merges: lru-clock-ticker (#32,#36), commandutil timeout (#40), build-logs-shared-app (#37).
- **Build simplification**: done except query graph (archived). User APPROVED (2026-10-13) one dep-prune PR for freed go.mod/npm deps once the source PRs merge (#18 sendgrid x2, #35 jwx+6, #20 faker, #29 plist, #42 cAdvisor, #40 go-ps, #36 uber atomic/shellwords/x-exp; dagre-d3-react only if graph lands). As of 2026-10-13 none merged; check with `GH_HOST=github.int.exe.xyz gh pr list`.
- **Ops notes**: workers use bb remote (memory: bb-remote-workers); local bazel fills 25GB disks. scratchpad prcheck.sh strips/blocks result.md before bin/pr (recreate if session scratchpad is gone).
- **Decided**: keep git_trace2 + exptest; unread flags left alone; build speed/remote exec dropped as a goal.
- **Earlier work stopped**: metrics-ext4/metrics-proxy/handoff-design/rewind-design; patches/results in archive/. PRs #6-#10 still open, untouched.
