# Worker environment (read first)

- This VM has 2 shared vCPUs and ~25 GB of disk. Local Bazel analysis takes 15+ minutes and
  has filled the disk on earlier workers. Do not run local `bazel build/test/query` over large
  target sets.
- **Build and test remotely:** `bash -lc 'cd ~/repo && bb remote test <targets> <flags>'` (a login shell
  sets BUILDBUDDY_API_KEY). It runs the whole invocation, including your local changes, on a
  BuildBuddy runner. The git remotes are already set up for it; do not change them. If it hasn't
  started within ~10 minutes, fall back to a narrow local `bazel test` and say which you used. Never
  claim something passes unless you ran it.
- **Bazel output is expensive to read.** Redirect it to a log file and read only what matters:
  `rg -n 'ERROR|FAIL|PASSED|NO STATUS|Executed' /tmp/x.log | tail -20`, not `tail -100`.
- **Waiting is expensive.** Each check re-reads your entire context. When a command will take more
  than 2 minutes, run it in the background and sleep at least 5 minutes between checks.
- **Read narrowly.** Use `rg -n` to find, then `sed -n 'A,Bp'` on the range. Don't cat whole large files,
  and don't re-read a file you already have in context.
- **Your report goes in ~/result.md** (outside the repo), and your final message should be that report.
  Never create or commit result.md, review.md or task.md inside the repo.
- Commit your work on the current branch with a clear message. Don't push unless the task says to.

---

# Plan a landable series for "cache proxy OCI fetcher talks directly to registries"

## Goal
The repo owner has draft PRs upstream in buildbuddy-io/buildbuddy that together move OCI
image fetching so the cache proxy's OCIFetcher talks directly to container registries
instead of going through the BuildBuddy app. The drafts are too large to land as they are.
Propose a **series of small, independently reviewable and mergeable PRs** that gets to the
same end state. This is an analysis task: **do not implement anything or open PRs.**

Relevant drafts (fetch with
`git fetch https://github.com/buildbuddy-io/buildbuddy.git pull/<N>/head:pr-<N>`):
- #13762 `oci-fetcher-flags` (base master, +17/-17): adds `--executor.use_remote_oci_fetcher` and `--executor.use_oci_cache` flags
- #13763 `oci-fetcher-in-process` (stacked on #13762, +747/-71, 12 files): local `OCIFetcher` service in the executor
- #13764 `reuse-http-transports` (base master, +94/-40): reuses HTTP clients for OCI registries and remote asset fetches
- #13772 `cache-proxy-oci-fetcher` (base master, +547/-8, 7 files): OCI fetcher in the cache proxy

Fetch upstream master too and diff each PR against its merge-base.

## What to produce
Write ~/result.md with:
1. **End state:** two or three sentences on what the four drafts do together, and how they depend on each other
   (shared files, overlaps, conflicts, anything duplicated between #13763 and #13772).
2. **Proposed series:** an ordered list of PRs. For each one, give:
   - title, the files involved, and the approximate size in lines
   - which draft hunks it takes (be concrete: file plus function or symbol)
   - its dependencies on earlier PRs in the series
   - whether it changes behaviour (aim for refactors or default-off flags first, and the behaviour flip last)
   - how a reviewer can test it
   Target: each PR under ~200 changed lines, with behaviour-neutral changes first.
3. **Gaps and risks:** anything the drafts are missing before they can land (tests, flag gating,
   auth/credentials handling when the proxy talks to registries, metrics, rollback), plus
   anything that looks wrong.
4. **Which drafts to keep, re-cut or close.**

## Budget
Read and diff only. At most ~60 tool calls. No Bazel builds or tests. Keep it concise:
result.md should be a plan someone can act on, under ~150 lines.
