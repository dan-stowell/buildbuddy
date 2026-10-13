# Replace SendGrid SDKs with direct HTTP requests

Email was the only first-party user of `github.com/sendgrid/sendgrid-go` and `github.com/sendgrid/rest`. Replace their request and payload helpers with small private JSON types and a direct POST through the existing injected `http.Client`, defaulting to BuildBuddy's instrumented HTTP client.

Preserve the SendGrid endpoint and headers, sender fallback, HTML content, validation, and one personalization per recipient so recipients remain private. Requests use the caller's context. Responses are read and closed, all 2xx statuses succeed, and transport/read failures and non-2xx responses retain Unavailable status handling and response-body error details.

Remove three SDK labels from the email BUILD. Extend the httptest coverage for HTTP 200, 300 and 401, canceled contexts, and unexpected JSON fields; existing cases verify authorization, content type, complete payload fields, individual recipient personalizations, sender selection, validation, and HTTP 500 handling.

| Email package metric | Before | After |
| --- | ---: | ---: |
| Bazel targets | 2 | 2 |
| Direct BUILD dependency labels | 9 | 6 |
| External BUILD dependency labels | 4 | 1 |
| SendGrid modules used | 2 | 0 |
| Source LOC | 160 | 188 |
| Test LOC | 270 | 312 |
| BUILD LOC | 28 | 25 |

Counts were measured with the same script against original and edited files. Binary size and transitive dependency savings were not measured. Repository search (`rg 'github.com/sendgrid/|@com_github_sendgrid_' --glob '*.go' --glob BUILD .`) finds no remaining first-party users. Both modules can be removed in the separate serialized dependency cleanup PR; shared dependency files are unchanged.

## Validation

- `git diff --check`: clean.
- `bazel test //enterprise/server/backends/email:email_test --jobs=2`: passed after applying the build-time analyzer's requested direct struct conversion.
- The scoped direct-dependent query returned email, email_test, sendmail_lib, notification, notification_test, usagealert_lib, and usagealert_test.
- All seven targets built successfully; all three test targets passed with:

```sh
bazel test \
  //enterprise/server/backends/email:email \
  //enterprise/server/backends/email:email_test \
  //enterprise/server/backends/email/sendmail:sendmail_lib \
  //enterprise/server/notification:notification \
  //enterprise/server/notification:notification_test \
  //enterprise/server/usagealert:usagealert_lib \
  //enterprise/server/usagealert:usagealert_test \
  --jobs=2
```

The full `rdeps(//..., //enterprise/server/backends/email:all, 1)` query failed on an unrelated Docker repository build timeout. A narrower query over all packages identified by the first-party BUILD reference search succeeded. The direct-dependent build initially exhausted disk space; pruning unused Docker build cache restored space, and the identical test command then passed.

Only email source, tests, and BUILD are included in the branch diff. Reports remain on disk and are excluded from branch history. No push or PR has been performed.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3