1. **Task accomplished.** The change replaces both reset subprocesses with quoted SQL on the control database, force-disconnects old sessions, and preserves migrations. Changes stay within scope; there are no uncommitted changes. Existing logs confirm 670 resets before and after, with aggregate reset time falling from 456.57s to 379.37s. Usage-specific timings remain noisy, as documented.

2. **Correctness bugs:** None found.

3. **Missing tests:** No blocking gaps. A focused regression test holding an active connection across reset would strengthen coverage of forced disconnection and database isolation. Existing logs confirm identical case counts: 146 passing cases, each passing 5/5 before and after, plus one unchanged ClickHouse-only skip. I inspected these logs; I did not rerun tests.

VERDICT: approve