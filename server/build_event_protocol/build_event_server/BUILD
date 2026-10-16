load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_test")

go_library(
    name = "build_event_server",
    srcs = ["build_event_server.go"],
    importpath = "github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_server",
    visibility = ["//visibility:public"],
    deps = [
        "//proto:build_events_go_proto",
        "//proto:publish_build_event_go_proto",
        "//server/environment",
        "//server/interfaces",
        "//server/metrics",
        "//server/real_environment",
        "//server/util/log",
        "//server/util/status",
        "@org_golang_google_grpc//:grpc",
        "@org_golang_google_protobuf//types/known/emptypb",
        "@org_golang_x_sync//errgroup",
    ],
)

go_test(
    name = "build_event_server_test",
    srcs = ["build_event_server_test.go"],
    deps = [
        ":build_event_server",
        "//proto:build_events_go_proto",
        "//proto:publish_build_event_go_proto",
        "//server/interfaces",
        "//server/testutil/testbes",
        "//server/testutil/testenv",
        "//server/util/status",
        "//server/util/uuid",
        "@com_github_stretchr_testify//assert",
        "@com_github_stretchr_testify//require",
    ],
)

go_test(
    name = "target_status_flush_test",
    size = "medium",
    srcs = ["target_status_flush_test.go"],
    deps = [
        ":build_event_server",
        "//proto:build_event_stream_go_proto",
        "//proto:build_events_go_proto",
        "//proto:invocation_status_go_proto",
        "//proto:publish_build_event_go_proto",
        "//server/build_event_protocol/build_event_handler",
        "//server/testutil/testauth",
        "//server/testutil/testclickhouse",
        "//server/testutil/testenv",
        "//server/interfaces",
        "//server/util/status",
        "//server/util/authutil",
        "//server/util/db",
        "//server/util/testing/flags",
        "//server/util/uuid",
        "@com_github_stretchr_testify//require",
        "@org_golang_google_protobuf//types/known/anypb",
    ],
)
