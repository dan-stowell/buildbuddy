module github.com/buildbuddy-io/oci-ext4-benchmark

go 1.27.2

require (
	github.com/Microsoft/hcsshim v0.14.1
	github.com/buildbuddy-io/buildbuddy v0.0.0
	github.com/google/go-containerregistry v0.21.7
)

require (
	github.com/containerd/cgroups/v3 v3.0.5 // indirect
	github.com/docker/cli v29.5.3+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.9.3 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/klauspost/compress v1.19.1 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	go.opencensus.io v0.24.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gotest.tools/v3 v3.5.2 // indirect
)

replace github.com/buildbuddy-io/buildbuddy => ../../..
