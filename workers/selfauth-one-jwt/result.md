# Replace selfauth's jwx dependency with golang-jwt/v4

Selfauth was the only first-party user of `github.com/lestrrat-go/jwx`, while `github.com/golang-jwt/jwt/v4` was already used throughout the repository and present in selfauth's transitive dependency graph. This change signs RS256 tokens with golang-jwt/v4, stores `crypto/rsa` keys directly, and publishes an explicit RSA JWKS using `encoding/json`.

The fixed signing key, issuer, audience array, expiry, empty subject, identity claims, access-token hash, random JWT ID, token response fields, and HTTP content type are preserved. Neither the old JWT header nor the old JWKS contained a `kid`; that absence is preserved. The JWKS retains the original field order, indentation, unpadded base64url encoding, and absence of a trailing newline.

## Compatibility tests

- Compare the published JWKS byte-for-byte with a golden response captured using jwx v1.2.29 and the original RSA parameters.
- Decode the published JWKS as a client would and verify the issued token through both golang-jwt/v4 and independent `crypto/rsa.VerifyPKCS1v15` verification.
- Check the JWT header, all ten claims, expiry and issuance times, random ID encoding, and token endpoint response fields.

## Dependency reduction

Before and after measurements use the same command: `bazel query 'deps(//enterprise/server/selfauth:selfauth)'`. Dependency-label counts include source-file and metadata labels, not just compiled libraries; external repositories are counted by unique external-label prefixes.

| Measure | Before | After | Change |
| --- | ---: | ---: | ---: |
| Transitive dependency labels | 4,835 | 4,692 | -143 |
| External dependency labels | 4,488 | 4,345 | -143 |
| External repositories | 105 | 99 | -6 |
| Direct library dependencies | 9 | 7 | -2 |
| Direct external library dependencies | 4 | 2 | -2 |
| Declared Go targets | 1 | 2 | +1 test |
| Production source lines (`wc -l selfauth.go`) | 268 | 257 | -11 |

The after dependency set is a strict subset of the before set. The six repositories removed from selfauth's graph are jwx and its five lestrrat helpers: backoff/v2, blackmagic, httpcc, iter, and option. The new tests add 125 Go source lines and a nine-line golden fixture.

Repository searches find no remaining first-party Go or BUILD references to jwx, its five lestrrat helpers, or `github.com/goccy/go-json`. The latter was not in the baseline Bazel graph and is an additional candidate for Go module cleanup, rather than a seventh removed Bazel repository. Shared dependency files are unchanged; removal of unused module declarations is reserved for the separate serialized dependency-cleanup PR. Binary size was not measured.

## Validation

Passed with local execution and two build jobs:

```sh
bazel test --jobs=2 --remote_executor= //enterprise/server/selfauth:selfauth_test
bazel build --jobs=2 --remote_executor= \
  //enterprise/server/selfauth:selfauth \
  //enterprise/server/selfauth:selfauth_test \
  //enterprise/server/oidc:oidc \
  //enterprise/server/cmd/server:server_lib
```

Both interoperability tests passed, and all four build targets passed with analyzer validation enabled. The scoped build completed in 3,303 seconds with 12,514 actions. `git diff --check` passed.

The repository-wide `bazel query 'rdeps(//..., //enterprise/server/selfauth:selfauth, 1)'` failed while preloading an unrelated execution-image repository because its Docker build timed out. A source/BUILD search identified OIDC and the enterprise server library as the existing direct users. A focused query over those targets, selfauth, and its new test passed and returned exactly the four targets built above:

```sh
bazel query 'rdeps(set(//enterprise/server/cmd/server:server_lib //enterprise/server/oidc:oidc //enterprise/server/selfauth:selfauth //enterprise/server/selfauth:selfauth_test), //enterprise/server/selfauth:selfauth, 1)'
```

The PR changes only selfauth source, BUILD, tests, and the JWKS test fixture. This write-up is a local, untracked file and is excluded from branch history.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3