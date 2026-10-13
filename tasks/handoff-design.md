# Task (research/design, no PR): what would it take to make hinted handoffs in the distributed cache never drop?

## Context
The BuildBuddy distributed cache (`enterprise/server/backends/distributed/` and its helpers; find the hinted-handoff code by grepping for "hint"/"handoff") uses hinted handoff: when a replica peer is unavailable during a write, another peer keeps a hint and later forwards the data. The repo owner (GitHub user **dan-stowell**) has several open PRs that make dropped handoffs less likely but don't eliminate them.

## Step 1: find and read the PRs
List open PRs authored by dan-stowell that touch hinted handoff, in both `dan-stowell/buildbuddy` and upstream `buildbuddy-io/buildbuddy`. Try `gh pr list --repo <repo> --author dan-stowell --state open` (set `GH_HOST=github.int.exe.xyz` for the fork if github.com auth isn't available), or read the public PR pages. For each one, fetch the branch and summarize what it changes. Do not push, comment on, or modify any PR.

## Step 2: enumerate every way a handoff can still be lost
Work from master plus the PRs combined. Trace the full lifecycle: write path → choosing the hint holder → storing the hint → retry/delivery → ack/deletion. For each **remaining** loss path, give file:line, the trigger, and how likely it is. Consider at least these:
- **Hint storage:** in-memory vs on disk, and bounded-queue overflow or eviction.
- **Hint holder:** the holder restarts, crashes, or loses its disk before delivery.
- **Expiry:** retry give-up or TTL expiry, and the target staying down longer than the hints last.
- **Membership:** the target is removed, replaced, or its peer set changes while hints are pending.
- **Write ack:** the client write is acked before the hint is durable.
- **Concurrency:** races between delivery and later writes, deletes, or evictions.
- **Delivery accounting:** delivery that succeeds but is never confirmed (so a duplicate is sent), or the reverse.
- **Holder eviction:** eviction on the hint holder removes the data the hint points at.

## Step 3: design options to eliminate them
For each loss path, give the mechanism that closes it. Candidates include:
- **Durable hint log:** write and fsync it before acking the client.
- **Backpressure, not drops:** slow writes or fail them over, never drop hints.
- **Replicate the hints themselves.**
- **Fail the write:** count a write as successful only if W durable copies or hints exist (sloppy quorum).
- **Pin data with pending hints:** don't evict it until delivered.
- **Retry with no TTL:** handle membership changes by re-routing hints to the new owner.
- **Anti-entropy backstop:** periodic Merkle or digest-range sync between replicas, or read repair, to catch anything that slips through.
State clearly which losses **cannot** be eliminated by hints alone. An example is a hint holder's disk dying before delivery while the original target is also down. Say what guarantee is achievable: "no loss unless more than N simultaneous disk failures" versus "eventually repaired by anti-entropy". Also note that a cache can tolerate some loss, and say where the right trade-off sits.

## Step 4: cost and plan
For each mechanism, estimate the cost in write latency, disk IO, memory, and code complexity. Propose an incremental plan, ordered by loss eliminated per unit of effort, with how to measure progress, including metrics for hints created, delivered, dropped, and expired, and their reasons. Note which pieces are already present in master or the PRs.

## Deliverable
Write `result.md` as a self-contained design document (no code changes needed). Prototype code is optional and only for validating a claim; if you write any, commit it, but it won't be opened as a PR.
