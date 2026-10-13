1. **Task accomplished.** Both requested tests now start one OSS app per parent, saving two startups. Workspaces, repos, and runners remain per child; cases and assertions are preserved. `result.md` documents isolation and measured savings averaging 6.47 seconds (~9%).

2. **Correctness bugs:** None found in the committed diff. There are no uncommitted changes.

3. **Missing tests:** No additional tests needed for this setup change. I verified the existing before/after logs: all four child cases passed 5/5 times in each phase, with identical case lists. I did not rerun tests or the full integration target.

VERDICT: approve
