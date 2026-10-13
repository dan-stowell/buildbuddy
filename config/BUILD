# gazelle:default_visibility //visibility:public
package(default_visibility = ["//visibility:public"])

exports_files(["buildbuddy.local.yaml"])

filegroup(
    name = "config_files",
    srcs = select({
        "//:release_build": ["buildbuddy.release.yaml"],
        "//conditions:default": glob(["**"]),
    }),
)
