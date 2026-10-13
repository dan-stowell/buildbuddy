1. **Task accomplished.** The original timeout bodies are byte-identical (independently verified). The change takes the preferred alternative: creates a persistent container and tests `Exec` timeout while retaining all five assertions and separate `Run` coverage. Existing remote logs confirm 5/5 runs passed before and after, with a 9.90-second median saving. Test-case sets remain unchanged; one pre-existing skipped test remains skipped.

2. **Correctness bugs:** None found.

3. **Missing tests:** None required for this change. The converted test covers deadline errors, negative exit code, captured stdout/stderr, and output-file preservation.

No files were modified during review.

VERDICT: approve