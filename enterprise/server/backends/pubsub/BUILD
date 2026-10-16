load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_test")

package(default_visibility = ["//enterprise:__subpackages__"])

go_library(
    name = "pubsub",
    srcs = ["pubsub.go"],
    importpath = "github.com/buildbuddy-io/buildbuddy/enterprise/server/backends/pubsub",
    deps = [
        "//enterprise/server/util/redisutil",
        "//server/interfaces",
        "//server/util/alert",
        "//server/util/flag",
        "//server/util/log",
        "//server/util/status",
        "@com_github_go_redis_redis_v8//:redis",
    ],
)

go_test(
    name = "pubsub_test",
    size = "small",
    srcs = ["pubsub_test.go"],
    embed = [":pubsub"],
    deps = [
        "//enterprise/server/testutil/testredis",
        "//enterprise/server/util/redisutil",
        "//proto:buildbuddy_service_go_proto",
        "//proto:eventlog_go_proto",
        "//proto:invocation_status_go_proto",
        "//server/backends/chunkstore",
        "//server/backends/memory_kvstore",
        "//server/buildbuddy_server",
        "//server/interfaces",
        "//server/tables",
        "//server/testutil/testenv",
        "//server/testutil/testleak",
        "//server/util/status",
        "//server/util/testing/flags",
        "@com_github_go_redis_redis_v8//:redis",
        "@com_github_stretchr_testify//assert",
        "@com_github_stretchr_testify//require",
    ],
)
