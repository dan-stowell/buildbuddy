load("@io_bazel_rules_go//go:def.bzl", "go_binary")

go_binary(
    name = "ext4_bench",
    srcs = ["main.go"],
    visibility = ["//visibility:public"],
    target_compatible_with = ["@platforms//os:linux"],
    deps = [
        "@com_github_google_go_containerregistry//pkg/name",
        "@com_github_google_go_containerregistry//pkg/v1:pkg",
        "@com_github_google_go_containerregistry//pkg/v1/mutate",
        "@com_github_google_go_containerregistry//pkg/v1/remote",
        "@org_golang_x_sys//unix",
    ],
)
