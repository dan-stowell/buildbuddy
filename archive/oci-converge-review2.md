1. **Task completion:** Acceptable under the explicit skip allowance. Probers’ two Docker rules are removed; the other five packages are skipped because their OCI bases differ from their legacy bases, as verified in the dependency definitions and explained in `result.md`. No removed-label callers were found.

2. **Correctness bugs:** None found. The retained image preserves the file inputs, entrypoint, and base digest. The explicit `0555` mapping addresses the reported permission mismatch.

3. **Missing tests:** No blocking gap. `result.md` reports successful builds and config/payload comparisons, but the comparison script is not retained for repeatable regression checks. I did not rerun those builds; the full reverse-dependency query also remains unverified.

`result.md` is ignored and uncommitted, so its validation evidence and skip rationale will not accompany the branch unless included separately.

VERDICT: approve
