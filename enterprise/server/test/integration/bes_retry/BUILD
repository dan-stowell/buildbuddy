load("@io_bazel_rules_go//go:def.bzl", "go_test")

package(default_visibility = ["//enterprise:__subpackages__"])

go_test(
    name = "bes_retry_test",
    size = "medium",
    srcs = ["bes_retry_test.go"],
    tags = ["block-network"],
    deps = [
        "//enterprise/server/testutil/buildbuddy_enterprise",
        "//proto:buildbuddy_service_go_proto",
        "//proto:invocation_go_proto",
        "//proto:invocation_status_go_proto",
        "//server/testutil/testbazel",
        "//server/testutil/testfs",
        "@com_github_google_uuid//:uuid",
        "@com_github_stretchr_testify//assert",
        "@com_github_stretchr_testify//require",
    ],
)
