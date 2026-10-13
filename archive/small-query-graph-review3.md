1. **Task completion:** Mostly accomplished. The change replaces the sole `dagre-d3-react` import with native SVG and an in-repo layout, preserves the guard and summaries, and leaves dependency files unchanged. Saved build/test logs report success and an 875,720-byte bundle reduction. Edge routing still has a correctness regression.

2. **Correctness bug — P2:** [query_graph_layout.ts:129](/home/exedev/repo/app/invocation/query_graph_layout.ts:129) routes adjacent-column edges using a Bézier curve that can pass through unrelated nodes in the source column. I reproduced this with nodes `a`, a 100-character isolated label, `b`, `c`, `d`, and edges `a → b/c/d`. The `a → d` path intersects the isolated node’s rectangle at approximately `(469, 122)`. Because rectangles render over edges, the link becomes obscured and appears to enter the unrelated node. Route adjacent-column links through clear column gaps, accounting for column widths.

3. **Missing tests:** Add geometry coverage for adjacent-column edges with unequal label widths and multiple targets. Mounted browser tests are also missing for wheel zoom, pointer pan, pinch, and resize fitting; the current zoom test verifies only the helper’s arithmetic.

No files were modified. I reproduced the routing bug using the current layout code; build/test results above come from existing validation logs.

VERDICT: changes-needed
