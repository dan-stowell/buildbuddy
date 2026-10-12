load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_test")

go_library(
    name = "tarstream",
    srcs = ["tarstream.go"],
    importpath = "github.com/buildbuddy-io/buildbuddy/enterprise/server/util/ext4/tarstream",
    visibility = ["//enterprise:__subpackages__"],
)

go_test(
    name = "tarstream_test",
    srcs = ["tarstream_test.go"],
    embed = [":tarstream"],
)
