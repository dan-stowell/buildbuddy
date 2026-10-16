load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_test")

go_library(
    name = "testleak",
    testonly = 1,
    srcs = [
        "fd.go",
        "growth.go",
        "soak.go",
        "testleak.go",
    ],
    importpath = "github.com/buildbuddy-io/buildbuddy/server/testutil/testleak",
    visibility = ["//visibility:public"],
    deps = ["@org_uber_go_goleak//:goleak"],
)

go_test(
    name = "testleak_test",
    size = "medium",
    srcs = [
        "gc_test.go",
        "helpers_test.go",
        "soak_test.go",
        "testleak_test.go",
    ],
    data = ["//server/testutil/testleak/netpollhelper"],
    embed = [":testleak"],
    x_defs = {"netpollHelperRlocationpath": "$(rlocationpath //server/testutil/testleak/netpollhelper)"},
    deps = [
        "@com_github_stretchr_testify//require",
        "@io_bazel_rules_go//go/runfiles",
        "@org_golang_google_grpc//:grpc",
        "@org_golang_google_grpc//codes",
        "@org_golang_google_grpc//credentials/insecure",
        "@org_golang_google_grpc//health",
        "@org_golang_google_grpc//health/grpc_health_v1",
        "@org_golang_google_grpc//status",
        "@org_uber_go_goleak//:goleak",
    ],
)

