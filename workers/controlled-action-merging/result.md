Stopped as instructed: Remote Bazel measured the original test at **11.02s**, with cleanup canceling the command after roughly 10s. The claimed ~60s saving is incorrect.

Reverted draft changes. Both baseline runs passed. Findings and case lists are in [result.md](/home/exedev/repo/result.md).

Committed as `135859d68`. No push or PR.