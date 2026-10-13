# Replace the invocation query graph dependency with native SVG

The invocation query graph was the only first-party importer of `dagre-d3-react`, which bundles its own d3/dagre implementation. Replace it with native SVG and a small, dependency-free layered layout. Preserve node labels, directed links, viewport fitting, wheel/double-click zoom, pointer pan and touch pinch, the >1,000-edge guard with Show anyway, and existing graph summaries and insights.

Route every edge orthogonally through column gutters. Horizontal endpoint segments stay in the endpoint's row; vertical turns happen beyond the widest node in the column. Adjacent columns share a gutter; other links use a lane above every node. This general routing handles adjacent-column fanout, skip-layer links, back edges, and self-loops without crossing unrelated node rectangles. It remains linear in nodes and edges. Links may share gutter segments, so dense graphs can still have overlapping edges.

Geometry tests check exact segment/rectangle intersections for the reported triangle, the 100-character isolated-node fanout repro, and 300 deterministic randomized small DAGs with shuffled declarations, varying label widths/heights, isolated nodes, and varying edge density. Each randomized graph is also checked after adding a cycle and self-loop.

Reuse the existing graph CSS. Keep the renderer in the card's existing Bazel target, with one small layout library and two Jasmine test targets. The layout retains cycles/self-loops and uses iterative traversals for long dependency chains. Node positions and edge routing differ from dagre's layout; edge labels remain unimplemented, matching the existing TODO.

Memoize layout using a collision-free serialized key of node IDs and edge endpoints rather than array identity. Unchanged graph content from invocation log/model rerenders now preserves zoom/pan. Auto-fit still runs initially, when graph content or measured font changes, and on actual viewport resize; duplicate observer notifications at unchanged dimensions do not refit.

## Bundle impact

Both baseline and final bundles were built with `bb remote` using the same app target and fastbuild configuration. Measurements count `app.js` bytes and gzip level 9 bytes with `mtime=0`; source maps are excluded.

| Measurement | Before | After | Reduction |
| --- | ---: | ---: | ---: |
| Unminified JavaScript bytes | 26,200,283 | 25,324,603 | 875,680 (3.34%) |
| Gzip bytes | 3,544,226 | 3,362,180 | 182,046 (5.14%) |
| Card direct npm dependency labels | 6 | 4 | 2 |
| First-party dagre-d3-react importers | 1 | 0 | 1 |

The original survey's 6.7% share was outdated for this checkout. The measured saving is immediate even while package declarations remain. Baseline commit: `770b88bc89c0b20059d42a237e8c7923ff5b7215`. Downloaded final JS: `/tmp/query-graph-review-app-after.js`; measurements: `/tmp/query-graph-review-sizes.json`.

## Validation

All review-follow-up builds/tests ran remotely from `~/repo`; no local Bazel build/test was used.

```sh
bash -lc "cd ~/repo && bb remote test //enterprise/app:app_bundle //app/invocation:query_graph_layout_test //app/invocation:invocation_query_graph_card_test //app/invocation:query_graph_layout_typecheck //app/invocation:invocation_query_graph_card_typecheck --test_output=errors"
bash -lc "cd ~/repo && bb remote build //enterprise/app:app_bundle"
bash -lc "cd ~/repo && bb remote --run_from_commit=770b88bc89c0b20059d42a237e8c7923ff5b7215 build //enterprise/app:app_bundle"
```

All three commands passed. The first built the bundle and both type-check targets and passed both test targets (10 layout specs and 6 card/viewport/zoom specs). Coverage includes dependency ordering, shared dependencies, isolated/implicit nodes, link deduplication, label dimensions, cycles/self-loops, skip-layer routing, a 12,000-edge chain, summary/insight counts, the large-graph guard boundary, Show anyway, and cursor-anchored zoom at scale limits.

- [Remote tests and type checks](https://app.buildbuddy.io/invocation/2f266578-12a7-44fb-b952-917f26f2495b)
- [Final bundle build](https://app.buildbuddy.io/invocation/079217f8-c30f-4b82-b423-bc640a16c68b)
- [Baseline bundle build](https://app.buildbuddy.io/invocation/64d895e8-f1f2-490b-932b-f7c6b57e1a28)

Prettier and `git diff --check` passed. Mounted wheel/pan/resize browser tests were not added: the existing TypeScript Jasmine harness runs in Node, the lockfile has no DOM test environment, and the existing browser harness is Go WebDriver-based. A Node hook-lifecycle regression test now executes the production renderer, memo/effect dependencies, wheel listener, pointer pan handlers, and resize callback with mocked browser APIs. It verifies zoom/pan survives parent rerenders with fresh unchanged graph arrays, an unchanged-size observer notification preserves the viewport, and changed content or actual resize refits. Real browser mounting/event dispatch remains unverified.

The earlier full-repository rdeps query hit an unrelated execution-image Docker repository timeout. The app-scoped rdeps query succeeded and its 11 affected target/typecheck labels were built successfully during the initial implementation validation.

## Dependency and PR scope

`dagre-d3-react` and `@types/dagre-d3` have no remaining TS/TSX imports or app BUILD references. The app-scoped direct-rdeps query for those two npm labels returned empty results. No standalone dagre implementation was present in the lockfile, and no packages were added. Leave package.json, all lockfiles, and other shared dependency files unchanged for the separate dependency-removal PR.

The branch history contains no file named `result.md` in any directory. `git diff --name-only origin/master...HEAD` lists only `app/invocation/BUILD` and the five graph/card implementation/test TS/TSX files. This write-up remains on disk at `~/repo/result.md`, untracked and locally ignored; it is not part of the commit or PR diff. Origin's fetch and push URLs are both `https://github.int.exe.xyz/dan-stowell/buildbuddy.git`. Nothing was pushed and no PR was opened.

---
Fixed in `2dcac7749`: unchanged graph content now preserves zoom/pan across parent rerenders. Added a renderer lifecycle regression test covering rerenders, content changes, and resize fitting.

`bb remote` tests, type checks, and bundle build passed. Verified code/BUILD-only diff and no `result.md` in branch history. Restored origin and updated [result.md](/home/exedev/repo/result.md), kept untracked. Nothing pushed.
