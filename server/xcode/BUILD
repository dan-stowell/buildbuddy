load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_test")

go_library(
    name = "xcode",
    srcs = [
        "xcode.go",
        "xcode_darwin.go",
        "xcode_plist.go",
    ],
    cgo = True,
    clinkopts = select({
        "@io_bazel_rules_go//go/platform:darwin": [
            "-framework CoreServices",
        ],
        "//conditions:default": [],
    }),
    importpath = "github.com/buildbuddy-io/buildbuddy/server/xcode",
    visibility = ["//visibility:public"],
    deps = select({
        "@io_bazel_rules_go//go/platform:darwin": [
            "//server/util/alert",
            "//server/util/flag",
            "//server/util/log",
            "//server/util/status",
        ],
        "//conditions:default": [],
    }),
)

go_test(
    name = "xcode_test",
    srcs = [
        "xcode_darwin_test.go",
        "xcode_plist_test.go",
    ],
    embed = [":xcode"],
    deps = select({
        "@io_bazel_rules_go//go/platform:darwin": [
            "@com_github_stretchr_testify//require",
        ],
        "//conditions:default": [],
    }),
)
