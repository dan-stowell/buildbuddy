load("@io_bazel_rules_go//go:def.bzl", "go_test")

package(default_visibility = ["//enterprise:__subpackages__"])

go_test(
    name = "verify_cas_integrity_test",
    size = "medium",
    srcs = ["verify_cas_integrity_test.go"],
    deps = [
        "//enterprise/server/backends/pebble_cache",
        "//proto:remote_execution_go_proto",
        "//server/remote_cache/byte_stream_server",
        "//server/remote_cache/cachetools",
        "//server/remote_cache/content_addressable_storage_server",
        "//server/remote_cache/digest",
        "//server/testutil/testenv",
        "//server/testutil/testfs",
        "//server/util/compression",
        "//server/util/prefix",
        "//server/util/testing/flags",
        "@com_github_stretchr_testify//require",
        "@org_golang_google_genproto_googleapis_bytestream//:bytestream",
        "@org_golang_google_grpc//:grpc",
        "@org_golang_google_grpc//status",
    ],
)
