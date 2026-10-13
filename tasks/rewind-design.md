# Task (research/design, no PR): can Bazel action rewinding let the BuildBuddy cache relax blob-presence guarantees?

## Idea
Bazel can recover when a remote blob it expected is missing. With build-without-the-bytes (`--remote_download_minimal/toplevel`), it can rewind: re-run the action that produced the lost blob, instead of failing the build or retrying the whole build. If clients reliably do that, the cache may not need to work as hard to guarantee that every blob referenced by an ActionResult stays present. That could save IO, replication, and complexity. Find out whether that's true, for which clients, and what exactly could be relaxed.

## Part 1: what Bazel actually does (cite source)
Clone `https://github.com/bazelbuild/bazel` and answer with file references and the versions involved:
- **Flags and versions.** Which flags and versions enable rewinding of lost remote inputs: `--rewind_lost_inputs`, `--experimental_remote_cache_eviction_retries`, and anything else. What is the default in each of 7.x, 8.x, 9.x and HEAD? Is it compatible with BwoB, Skymeld, and `--keep_going`?
- **Trigger points.** Which lost-blob situations trigger rewinding? Consider each of:
  - (a) a local action whose remote input is missing on download;
  - (b) remote execution returning FAILED_PRECONDITION with missing-digest violations for inputs that came from earlier actions;
  - (c) top-level output download failing;
  - (d) an AC hit whose outputs are gone.
  For each, say whether Bazel rewinds, retries the whole build, re-uploads, or fails.
- **Limits.** Are there caps on rewinding (attempts, action types)? Which situations are not recoverable, such as outputs of non-hermetic or non-rewindable actions, test outputs, or runfiles trees?
- **Missing AC outputs.** How does Bazel treat an AC entry whose outputs are missing, and does it depend on the server validating them?

## Part 2: where BuildBuddy currently pays to guarantee presence
In this repo, find the mechanisms that exist mostly so referenced blobs stay present, with file:line for each:
- AC result validation on GetActionResult, i.e. checking the CAS for outputs;
- the semantics of FindMissingBlobs;
- access-time and TTL updates (lease extension) that keep blobs alive during a build;
- the replication factor and hinted handoff in the distributed cache (`enterprise/server/backends/distributed`);
- eviction policies that try to protect recently referenced blobs;
- anything in the cache proxy or the executor that pins or prefetches blobs.

Estimate what each one costs: RPCs, disk or network IO, and complexity.

## Part 3: what could be relaxed, and the risks
For each mechanism, state what could be relaxed if the client rewinds, and what breaks if it doesn't. Cover:
- **Other clients:** older Bazel, Buck2, Pants, `bb`, and BuildBuddy's own executors, which need action inputs present.
- **Other consumers:** UI artifact downloads, test logs, and the invocation/target pages.
- **Remote execution input trees.**
- **Cost of a rewind:** the build time a rewind costs compared with the IO saved. Rewinds also cause cascades, because re-running one action can require its own inputs again.

Propose how the server could detect rewind-capable clients per request: `RequestMetadata.tool_details` version, a header or flag, or opt-in per org. Then apply relaxed policies only to those clients.

## Part 4: recommendation and measurement
Give the concrete relaxations worth trying, ranked by savings over risk. For each, list the metrics needed first, such as how often missing blobs are hit per client version, rewind counts from build events if Bazel reports them in BEP, and AC validation failure rates. Describe a rollout plan.

Relate this to sibling work: a separate design task is examining how to make hinted handoffs never drop. Say explicitly whether rewinding makes that work less necessary, and for which clients.

## Deliverable
`result.md` is a self-contained design document. No PR. Clearly mark any claim not verified in source as unverified.
