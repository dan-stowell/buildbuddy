1. **Task accomplished:** Six scenarios are now top-level tests using a shared parameter helper. Resources and cleanup are scoped per test; all original assertions remain. Existing four-shard configuration suffices.

2. **Correctness bugs:** None found in the branch diff. No uncommitted changes exist.

3. **Missing tests:** No blocking gaps. Saved remote logs confirm all six scenarios passed 5/5 repetitions and the full target passed with one unchanged opt-in skip. I did not rerun tests. A paired, unfiltered before/after benchmark would better establish whole-target savings; current timings demonstrate improvement only for the networking matrix.

No files were modified.

VERDICT: approve