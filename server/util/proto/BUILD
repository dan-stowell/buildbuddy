load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_test")

go_library(
    name = "proto",
    srcs = ["proto.go"],
    importpath = "github.com/buildbuddy-io/buildbuddy/server/util/proto",
    visibility = ["//visibility:public"],
    deps = ["@org_golang_google_protobuf//proto"],
)

go_test(
    name = "proto_test",
    srcs = ["proto_test.go"],
    deps = [
        ":proto",
        "//proto:cache_go_proto",
        "//proto:distributed_cache_go_proto",
        "//proto:storage_go_proto",
        "@com_github_stretchr_testify//require",
        "@org_golang_google_genproto_googleapis_rpc//status",
        "@org_golang_google_protobuf//encoding/prototext",
        "@org_golang_google_protobuf//proto",
        "@org_golang_google_protobuf//reflect/protoreflect",
        "@org_golang_google_protobuf//types/known/anypb",
        "@org_golang_google_protobuf//types/known/wrapperspb",
    ],
)
