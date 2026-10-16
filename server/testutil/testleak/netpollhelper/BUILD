load("@io_bazel_rules_go//go:def.bzl", "go_binary", "go_library")

go_library(
    name = "netpollhelper_lib",
    testonly = 1,
    srcs = ["main.go"],
    importpath = "github.com/buildbuddy-io/buildbuddy/server/testutil/testleak/netpollhelper",
    visibility = ["//visibility:private"],
    deps = ["//server/testutil/testleak"],
)

go_binary(
    name = "netpollhelper",
    testonly = 1,
    embed = [":netpollhelper_lib"],
    visibility = ["//server/testutil/testleak:__pkg__"],
)
