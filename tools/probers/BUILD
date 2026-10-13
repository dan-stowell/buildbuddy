load("@rules_oci//oci:defs.bzl", "oci_image", "oci_push")
load("@rules_pkg//pkg:mappings.bzl", "pkg_attributes", "pkg_files")
load("@rules_pkg//pkg:tar.bzl", "pkg_tar")

pkg_files(
    name = "image_files",
    srcs = [
        "//tools/probers/bazelcache",
        "//tools/probers/bazelrbe",
        "//tools/probers/remote_runner",
        "@cloudprober",
        "@net_busybox_busybox-linux-amd64//file:busybox",
    ],
    # Match the read-only executable modes of the legacy container image.
    attributes = pkg_attributes(mode = "0555"),
)

pkg_tar(
    name = "files_tar",
    srcs = [":image_files"],
)

oci_image(
    name = "probers_oci_image",
    base = "@bazel_oci_image_base",
    entrypoint = [
        "/cloudprober",
        "--logtostderr",
    ],
    target_compatible_with = [
        "@platforms//os:linux",
    ],
    tars = [
        "//server/util/bazel:bazel_binaries_tar",
        ":files_tar",
    ],
    visibility = ["//visibility:public"],
)

oci_push(
    name = "push_probers_oci_image",
    image = ":probers_oci_image",
    remote_tags = "//deployment:oci_tag.txt",
    repository = "gcr.io/flame-build/probers-oci",
)
