load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_test")

package(default_visibility = ["//enterprise:__subpackages__"])

go_library(
    name = "ociconv",
    srcs = ["ociconv.go"],
    importpath = "github.com/buildbuddy-io/buildbuddy/enterprise/server/oci/ociconv",
    target_compatible_with = ["@platforms//os:linux"],
    deps = [
        "//enterprise/server/util/ext4",
        "//enterprise/server/util/oci",
        "//proto:remote_execution_go_proto",
        "//server/interfaces",
        "//server/util/disk",
        "//server/util/flag",
        "//server/util/hash",
        "//server/util/log",
        "//server/util/lockmap",
        "//server/util/status",
        "//server/util/tracing",
        "//third_party/singleflight",
        "@com_github_google_go_containerregistry//pkg/v1:pkg",
        "@com_github_google_go_containerregistry//pkg/v1/mutate",
        "@org_golang_x_sys//unix",
    ],
)

go_test(
    name = "ociconv_test",
    srcs = ["ociconv_test.go"],
    target_compatible_with = ["@platforms//os:linux"],
    deps = [
        ":ociconv",
        "//enterprise/server/remote_execution/filecache",
        "//enterprise/server/testutil/testregistry",
        "//enterprise/server/util/ext4",
        "//enterprise/server/util/oci",
        "//proto:remote_execution_go_proto",
        "//server/testutil/testenv",
        "//server/testutil/testfs",
        "//server/util/claims",
        "//server/util/hash",
        "//server/util/testing/flags",
        "@com_github_google_go_cmp//cmp",
        "@com_github_google_go_containerregistry//pkg/v1/mutate",
        "@com_github_stretchr_testify//assert",
        "@com_github_stretchr_testify//require",
    ],
)

# Opt-in benchmark binary; run outside the sandbox as root (see result.md).
go_test(
    name = "io_benchmark_test",
    srcs = ["io_benchmark_test.go", "publication_metadata_test.go"],
    embed = [":ociconv"],
    tags = ["manual"],
    target_compatible_with = ["@platforms//os:linux"],
    deps = [
        "//enterprise/server/clientidentity",
        "//server/interfaces",
        "//enterprise/server/oci/ocifetcher",
        "//proto:oci_fetcher_go_proto",
        "//server/backends/disk_cache",
        "//server/testutil/testcache",
        "//server/util/claims",
        "//server/resources",
        "//proto:remote_execution_go_proto",
        "//enterprise/server/remote_execution/copy_on_write",
        "//enterprise/server/remote_execution/filecache",
        "//enterprise/server/util/oci",
        "//enterprise/server/util/ext4",
        "@org_golang_x_sys//unix",
        "@com_github_google_go_containerregistry//pkg/v1/empty",
        "//server/testutil/testenv",
        "//server/util/testing/flags",
        "@com_github_google_go_containerregistry//pkg/v1:pkg",
        "@com_github_stretchr_testify//require",
    ],
)
