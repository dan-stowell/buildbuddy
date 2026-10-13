# gazelle:default_visibility //enterprise:__subpackages__
package(default_visibility = ["//enterprise:__subpackages__"])

filegroup(
    name = "config_files",
    srcs = select({
        "//:release_build": [
            "buildbuddy.release.yaml",
            "executor.release.yaml",
        ],
        "//conditions:default": glob(["*.yaml"]),
    }),
)
