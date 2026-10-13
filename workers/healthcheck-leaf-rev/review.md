1. **Task accomplished.** The new leaf package contains the unchanged types and method; interfaces retains true type aliases, and healthcheck imports the leaf directly. Saved query outputs confirm dependencies dropped from **4,602 to 3,061**. Build logs confirm all 31 selected targets, including the three binary proxies, built successfully. The healthcheck tests passed.

2. **Correctness bugs:** None found. The aliases preserve type identity and existing method signatures. No uncommitted changes were present.

3. **Missing tests:** No blocking gaps for this mechanical extraction. Existing tests cover shutdown and signals; consumer builds verify compatibility. A focused `CheckerFunc.Check` forwarding test could provide extra coverage, but the method is unchanged.

The full-repository reverse-dependency query failed on a Docker fetch; the documented focused fallback is reasonable.

VERDICT: approve