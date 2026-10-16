package ociruntime_test

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/cgroup"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/container"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/containers/ociruntime"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/filecache"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testregistry"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/cpuset"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/oci"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testnetworking"
	"github.com/buildbuddy-io/buildbuddy/server/util/disk"
	"github.com/buildbuddy-io/buildbuddy/server/util/networking"
	"github.com/buildbuddy-io/buildbuddy/server/util/platform"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// This is a separate binary from ociruntime_test so that processes and pidfds
// left behind by unrelated tests cannot show up as growth here.

// Set via x_defs in BUILD file.
var crunRlocationpath, busyboxImageRlocationpath string

func init() {
	runtimePath, err := runfiles.Rlocation(crunRlocationpath)
	if err != nil {
		log.Fatalf("Failed to locate crun in runfiles: %s", err)
	}
	*ociruntime.Runtime = runtimePath
}

func TestMain(m *testing.M) {
	// Mirror executor startup, which enables the cgroup controllers that task
	// cgroups need in the parent cgroup of task cgroups.
	for _, controller := range cgroup.SetupControllers {
		if err := cgroup.WriteSubtreeControl(cgroup.RootPath, map[string]bool{controller: true}); err != nil {
			log.Printf("Could not enable cgroup controller %q for child cgroups of %q: %s", controller, cgroup.RootPath, err)
		}
	}
	os.Exit(m.Run())
}

// TestLifecycleResourceGrowth keeps the provider alive while completed tasks
// are removed, so provider-owned pools cannot hide per-task resource growth.
// Besides goroutines and FDs, it counts host resources that each container
// owns until Remove: mounts and bundles under the build root, crun state,
// cgroups, network namespaces and interfaces, and container processes.
func TestLifecycleResourceGrowth(t *testing.T) {
	for _, mode := range []string{"exec", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			flags.Set(t, "grpc_server_worker_multiplier", 0)
			require.NoError(t, networking.Configure(context.Background()))
			testnetworking.Setup(t)
			// Pooled networks are provider-owned; this test covers per-task teardown.
			flags.Set(t, "executor.oci.network_pool_size", 0)
			runtimeRoot := testfs.MakeTempDir(t)
			flags.Set(t, "executor.oci.runtime_root", runtimeRoot)

			// The registry is a process-lifetime fixture started before the
			// baseline. The image is pulled once, in the first warmup iteration.
			registry := testregistry.Run(t, testregistry.Opts{})
			registry.Push(t, testregistry.ImageFromRlocationpath(t, busyboxImageRlocationpath), "busybox", nil)
			image := registry.ImageAddress("busybox")

			ctx := context.Background()
			env := testenv.GetTestEnv(t)
			// Without this, every Run re-authenticates against the registry,
			// which measures the resolver transport instead of the container.
			env.SetImageCacheAuthenticator(container.NewImageCacheAuthenticator(container.ImageCacheAuthenticatorOpts{}))
			leaser, err := cpuset.NewLeaser(cpuset.LeaserOpts{})
			require.NoError(t, err)
			env.SetCPULeaser(leaser)
			flags.Set(t, "executor.cpu_leaser.enable", true)
			t.Cleanup(func() { require.Empty(t, leaser.TestOnlyGetOpenLeases()) })
			fc, err := filecache.NewFileCache(testfs.MakeTempDir(t), 10_000_000_000, false)
			require.NoError(t, err)
			t.Cleanup(func() { fc.Close() })
			fc.WaitForDirectoryScanToComplete()
			env.SetFileCache(fc)

			buildRoot, cacheRoot := testfs.MakeTempDir(t), testfs.MakeTempDir(t)
			provider, err := ociruntime.NewProvider(env, buildRoot, cacheRoot)
			require.NoError(t, err)
			wd := testfs.MakeDirAll(t, buildRoot, "work")

			counts := func() []int {
				mountinfo, err := os.ReadFile("/proc/self/mountinfo")
				require.NoError(t, err)
				mounts := 0
				for line := range strings.SplitSeq(string(mountinfo), "\n") {
					if strings.Contains(line, buildRoot+"/") {
						mounts++
					}
				}
				count := func(path, pattern string) int {
					matches, err := filepath.Glob(filepath.Join(path, pattern))
					require.NoError(t, err)
					return len(matches)
				}
				processes := 0
				pids, err := filepath.Glob("/proc/[0-9]*")
				require.NoError(t, err)
				for _, pid := range pids {
					root, _ := os.Readlink(filepath.Join(pid, "root"))
					cwd, _ := os.Readlink(filepath.Join(pid, "cwd"))
					if strings.HasPrefix(root, buildRoot+"/") || strings.HasPrefix(cwd, buildRoot+"/") {
						processes++
					}
				}
				return []int{mounts, count(filepath.Join(buildRoot, "executor/oci/run"), "*"), count(runtimeRoot, "*"), count("/sys/fs/cgroup", "*"), count("/var/run/netns", "*"), count("/sys/class/net", "*"), processes}
			}

			const warmup = 3
			var base []int
			testleak.CheckGrowth(t, testleak.Growth{Iterations: 10, Warmup: warmup}, func(i int) {
				c, err := provider.New(ctx, &container.Init{Props: &platform.Properties{ContainerImage: image}})
				require.NoError(t, err)
				func() {
					// Remove even when a step fails, as the runner does.
					defer func() { require.NoError(t, c.Remove(ctx)) }()
					if i == 0 {
						require.NoError(t, container.PullImageIfNecessary(ctx, env, c, oci.Credentials{}, image, false))
					} else {
						cached, err := c.IsImageCached(ctx)
						require.NoError(t, err)
						require.True(t, cached)
					}
					if mode == "exec" {
						require.NoError(t, c.Create(ctx, wd))
						res := c.Exec(ctx, &repb.Command{Arguments: []string{"true"}}, &interfaces.Stdio{})
						require.NoError(t, res.Error)
						require.Equal(t, 0, res.ExitCode)
						_, err := c.Stats(ctx)
						require.NoError(t, err)
						return
					}
					// Cancel the task once the container process is running.
					runCtx, cancel := context.WithCancel(ctx)
					defer cancel()
					done := filepath.Join(wd, "DONE")
					_ = os.Remove(done)
					exited := make(chan struct{})
					go func() {
						defer close(exited)
						if disk.WaitUntilExists(runCtx, done, disk.WaitOpts{Timeout: -1}) == nil {
							cancel()
						}
					}()
					res := c.Run(runCtx, &repb.Command{Arguments: []string{"sh", "-c", "touch DONE; sleep 1000000000"}}, wd, oci.Credentials{})
					cancel()
					<-exited
					require.True(t, status.IsCanceledError(res.Error), "%v", res.Error)
				}()

				got := counts()
				if i == warmup-1 {
					base = got
					return
				}
				if i < warmup {
					return
				}
				// Kernel process teardown can finish after crun returns. Poll
				// synchronously, so that no polling goroutine is counted.
				deadline := time.NewTimer(testleak.DefaultSettle)
				defer deadline.Stop()
				tick := time.NewTicker(20 * time.Millisecond)
				defer tick.Stop()
				for !slices.Equal(base, got) {
					select {
					case <-deadline.C:
						require.Equal(t, base, got, "mounts, bundles, crun state, cgroups, netns, interfaces, processes after measured iteration %d", i-warmup+1)
					case <-tick.C:
						got = counts()
					}
				}
			})
		})
	}
}
