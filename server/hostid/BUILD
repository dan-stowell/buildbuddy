load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_test")

go_library(
    name = "hostid",
    srcs = ["hostid.go"],
    importpath = "github.com/buildbuddy-io/buildbuddy/server/hostid",
    visibility = ["//visibility:public"],
    deps = [
        "//server/util/disk",
        "@com_github_google_uuid//:uuid",
    ],
)

go_test(
    name = "hostid_test",
    size = "small",
    srcs = ["hostid_test.go"],
    embed = [":hostid"],
    deps = [
        "//server/testutil/testleak",
        "@com_github_stretchr_testify//require",
    ],
)
