load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_test")

go_library(
    name = "protolet",
    srcs = ["protolet.go"],
    importpath = "github.com/buildbuddy-io/buildbuddy/server/http/protolet",
    visibility = ["//visibility:public"],
    deps = [
        "//server/util/proto",
        "//server/util/request_context",
        "@io_opentelemetry_go_otel_trace//:trace",
        "@org_golang_google_grpc//:grpc",
        "@org_golang_google_grpc//codes",
        "@org_golang_google_protobuf//encoding/protojson",
        "@org_golang_google_protobuf//encoding/prototext",
    ],
)

go_test(
    name = "protolet_test",
    size = "small",
    srcs = ["protolet_test.go"],
    embed = [":protolet"],
    deps = [
        "//server/testutil/testleak",
        "@com_github_stretchr_testify//require",
        "@org_golang_google_genproto_googleapis_bytestream//:bytestream",
        "@org_golang_google_grpc//:grpc",
    ],
)
