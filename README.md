# coord-notes

Records from the coordinator and its Codex workers on this fork. This is an orphan branch
with no shared history with master, so don't merge it.

- `findings.md`: one entry per task covering what was asked, what was found and the outcome. Start here.
- `ledger.md`: the coordinator's task table and open threads (a copy of the live ledger).
- `workers/<id>/`: each worker's task.md, result.md, review.md (for `-rev` clones), feedback.md and final.diff.
- `surveys/`: survey reports (deps, externals, dead code, test timing and patterns).
- `plans/`: plans for series of PRs (for example the OCI fetcher split).
- `archive/`: patches and results from stopped or dropped work.
- `tasks/`: the task specs as dispatched.

Full Codex transcripts stay on the coordinator VM under `coord/transcripts/`.
