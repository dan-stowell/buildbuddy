1. **Task completion:** Mostly accomplished. Survey is replaced with Promptui, BUILD dependencies are updated, and caching, Ctrl-C cancellation, and terminal restoration are covered. Shared dependency files remain unchanged. The existing picker tests passed when rerun.

2. **Correctness bugs:**
   - **P2 — Filter clearing regressed:** [remotebazel.go:242](/home/exedev/repo/cli/remotebazel/remotebazel.go:242). Survey cleared the filter on Ctrl-U/Ctrl-W; Promptui retains it. Reproduced: typing `origin`, Ctrl-U, Enter selects `origin` instead of restoring the full list and selecting `extra`.
   - **P2 — Navigation regressed:** [remotebazel.go:235](/home/exedev/repo/cli/remotebazel/remotebazel.go:235). Survey supported Tab to advance and arrow navigation wrapping at list boundaries. Promptui defaults preserve neither. Reproduced: Tab, Enter and Up, Enter both select `extra`; previously both selected `origin` in this two-remote fixture.

3. **Missing tests:** Add PTY cases for Ctrl-U/Ctrl-W filter clearing, Tab navigation, and boundary wrapping. Also cover search with no matches followed by recovery and Ctrl-C after typing a filter.

VERDICT: changes-needed
