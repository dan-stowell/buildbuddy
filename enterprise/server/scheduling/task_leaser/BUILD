load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_test")

package(default_visibility = ["//enterprise:__subpackages__"])

go_library(
    name = "task_leaser",
    srcs = ["task_leaser.go"],
    importpath = "github.com/buildbuddy-io/buildbuddy/enterprise/server/scheduling/task_leaser",
    deps = [
        "//proto:remote_execution_go_proto",
        "//proto:scheduler_go_proto",
        "//server/environment",
        "//server/interfaces",
        "//server/util/authutil",
        "//server/util/flag",
        "//server/util/log",
        "//server/util/proto",
        "//server/util/retry",
        "//server/util/status",
        "@org_golang_google_grpc//status",
    ],
)

go_test(
    name = "task_leaser_test",
    srcs = ["grpc_stop_semantics_test.go"],
    deps = [
        "//proto:scheduler_go_proto",
        "//server/testutil/testgrpc",
        "//server/util/grpc_client",
        "//server/util/grpc_server",
        "//server/util/status",
        "@com_github_stretchr_testify//require",
        "@org_golang_google_grpc//:grpc",
        "@org_golang_google_grpc//metadata",
        "@org_golang_google_grpc//status",
    ],
)
