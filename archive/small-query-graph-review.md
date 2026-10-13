1. **Task accomplishment:** Mostly. Replaces `dagre-d3-react` with native SVG and an in-repo layout, preserves the guard and summaries, adds layout tests, and leaves dependency files unchanged. Build/test logs support the reported 876,150-byte bundle reduction. No uncommitted changes were present.

2. **Correctness bug:**
   - **[P2] Edges can run through unrelated nodes** — [query_graph_layout.ts:114](/home/exedev/repo/app/invocation/query_graph_layout.ts:114). For `a → b`, `b → c`, and `a → c`, all three nodes share the same vertical center. The direct `a → c` curve becomes a horizontal line through `b`, overlapping both adjacent links. Since nodes render over edges, the direct dependency becomes indistinguishable from the chain. Route edges that skip layers around intermediate nodes.

3. **Missing tests:**
   - A layout test for edges skipping layers, checking that paths avoid intermediate node rectangles and remain distinguishable.
   - Mounted renderer tests for wheel zoom, pointer pan, pinch, pointer cancellation, and resize fitting. Existing tests exercise zoom arithmetic but never mount the renderer or dispatch its events.

VERDICT: changes-needed
