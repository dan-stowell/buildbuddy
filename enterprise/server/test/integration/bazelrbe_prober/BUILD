load("@io_bazel_rules_go//go:def.bzl", "go_test")

package(default_visibility = ["//enterprise:__subpackages__"])

go_test(
    name = "bazelrbe_prober_test",
    size = "medium",
    srcs = ["bazelrbe_prober_test.go"],
    data = [
        "//server/util/bazel:bazel-8.5.0_crossplatform",
        "//server/util/bazel:bazel-8.5.0_extract_installation",
        "//tools/probers/bazelrbe",
    ],
    x_defs = {
        "bazelrbeProberRunfilePath": "$(rlocationpath //tools/probers/bazelrbe)",
        "bazelBinaryRunfilePath": "$(rlocationpath //server/util/bazel:bazel-8.5.0_crossplatform)",
        "bazelOutdirRunfilePath": "$(rlocationpath //server/util/bazel:bazel-8.5.0_extract_installation)",
    },
    deps = [
        "//enterprise/server/test/integration/remote_execution/rbetest",
        "//server/util/testing/flags",
        "@com_github_stretchr_testify//require",
        "@io_bazel_rules_go//go/runfiles",
    ],
)
