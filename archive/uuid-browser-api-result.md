# UUID replacement: stopped to preserve behavior

No source or BUILD changes were made. The task's stop condition applies: a direct
replacement with `crypto.randomUUID()` would remove existing browser support.
This report is the only file added outside the two-file edit scope, as required
by the deliverable. Shared dependency files remain unchanged.

## Evidence

- `rg -n uuid app enterprise/app website` finds only the import and call in
  `app/shortcuts/shortcuts.tsx`, plus the two dependencies in
  `app/shortcuts/BUILD`. There are no other first-party UUID imports in those trees.
- The lockfile resolves `uuid` to 14.0.0. Its published `package/dist/v4.js`
  uses `crypto.randomUUID()` when available, otherwise generates a version 4
  UUID using `crypto.getRandomValues()` through `rng.js`.
- [MDN](https://developer.mozilla.org/en-US/docs/Web/API/Crypto/randomUUID)
  documents `randomUUID()` as requiring a secure context. Non-localhost HTTP
  origins do not provide it, whereas `getRandomValues()` is available there.
- `docs/config-app.md:31` documents `http://buildbuddy.acme.corp`, and
  `docs/config-samples.md:29` documents `http://acme.corp`. The proposed direct
  call would throw on such deployments when a shortcut is registered; the
  existing UUID fallback works there. No HTTPS-only deployment requirement
  was established, so the requested replacement cannot preserve that behavior.
- `curl -fsSL https://registry.npmjs.org/uuid/14.0.0` reports
  `dist.unpackedSize = 70265` bytes (68.6 KiB), rather than the estimated 77 KB.
  This is package size, not measured bundle savings. Removing the package was
  explicitly deferred, so no package-size gain is realized by this task.

## Before / after

The same commands were run before and after the decision to stop:

| Measurement | Before | After |
| --- | ---: | ---: |
| UUID-importing files in `app/`, `enterprise/app/`, `website/` (`rg -l 'from "uuid"' app enterprise/app website`) | 1 | 1 |
| Direct shortcuts BUILD dependencies (Python below) | 3 | 3 |
| Direct npm labels in those dependencies (Python below) | 2 | 2 |
| `shortcuts.tsx` LOC (Python below) | 200 | 200 |

```python
from pathlib import Path
b = Path('app/shortcuts/BUILD').read_text().split('    deps = [', 1)[1].split(']', 1)[0]
print('direct deps:', b.count('"//'))
print('npm deps:', b.count('"//:node_modules/'))
print('shortcuts.tsx LOC:', len(Path('app/shortcuts/shortcuts.tsx').read_text().splitlines()))
```

## Verification and omissions

- Inspected the exact published UUID 14.0.0 tarball with
  `tar -xOf /tmp/uuid.tgz package/dist/v4.js` and checked the MDN secure-context
  requirement. No runtime browser test was performed.
- Attempted `bazel query '//app/shortcuts:shortcuts'` and background
  `bazel query 'rdeps(//..., //app/shortcuts:shortcuts, 1)'`. Cancelled both after
  establishing the behavior blocker; the latter was waiting for the former.
  Neither yielded dependency-graph results.
- An attempted Node reproduction could not run because `node` is not on PATH.
- Did not run `bazel build //app:app_bundle` or tests: implementation stopped
  before edits under the task's explicit stop rule, so there is no changed
  executable behavior to validate.
- No external module became unused: UUID and its type dependency remain used
  by shortcuts. No dependency, lockfile, source, or BUILD edits were made.
- Did not add a fallback implementation or require HTTPS; either would change
  the requested scope. Did not push or open a PR.

