// Package bazelrbe_prober_test tests the bazelrbe prober against a local RBE stack.
package bazelrbe_prober_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/test/integration/remote_execution/rbetest"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
)

// Injected via x_defs in BUILD file
var (
	bazelrbeProberRunfilePath string
	bazelBinaryRunfilePath    string
	bazelOutdirRunfilePath    string
)

func TestBazelRBEProber(t *testing.T) {
	// Set up the local RBE environment
	env := rbetest.NewRBETestEnv(t)
	env.AddBuildBuddyServer()
	// Run the executor with an API key to more closely match a production setup.
	flags.Set(t, "executor.api_key", env.APIKey1)
	env.AddExecutor(t)

	// Get the prober binary path from runfiles
	proberPath, err := runfiles.Rlocation(bazelrbeProberRunfilePath)
	require.NoError(t, err, "failed to locate bazelrbe prober binary")

	// Get the bazel binary path from runfiles
	bazelPath, err := runfiles.Rlocation(bazelBinaryRunfilePath)
	require.NoError(t, err, "failed to locate bazel binary")

	// Get the pre-warmed bazel outdir (contains install_base, repository_cache, and MODULE.bazel.lock)
	outdirPath, err := runfiles.Rlocation(bazelOutdirRunfilePath)
	require.NoError(t, err, "failed to locate bazel outdir")

	origInstallBase := filepath.Join(outdirPath, "install_base")
	repoCache := filepath.Join(outdirPath, "repository_cache")

	// Copy the install_base to a writable location because bazel needs to acquire
	// a lock file in the install_base directory, and the runfiles may be read-only.
	// We need to dereference symlinks to get the actual directory content.
	tmpDir := t.TempDir()
	installBase := filepath.Join(tmpDir, "install_base")

	// Resolve any symlinks in the install_base path to get the real directory.
	// Runfiles often use relative symlinks, and we need the absolute real path.
	realInstallBase, err := filepath.EvalSymlinks(origInstallBase)
	require.NoError(t, err, "failed to resolve install_base path")
	cpCmd := exec.Command("cp", "-R", realInstallBase, installBase)
	cpOut, err := cpCmd.CombinedOutput()
	require.NoError(t, err, "failed to copy install_base to writable location: %s", string(cpOut))

	// Set file mtimes to the future so bazel sees this as a pristine install
	mtime := time.Now().Add(10 * 365 * 24 * time.Hour).Format("2006-01-02T00:00:00")
	findCmd := exec.Command("find", ".", "-type", "f", "-exec", "touch", "-d", mtime, "{}", "+")
	findCmd.Dir = installBase
	out, err := findCmd.CombinedOutput()
	require.NoError(t, err, "failed to update install_base mtimes: %s", string(out))

	// Make the install_base recursively writable so that cleanup can remove it.
	// Some files in the install_base have restrictive permissions.
	chmodCmd := exec.Command("chmod", "-R", "u+w", installBase)
	out, err = chmodCmd.CombinedOutput()
	require.NoError(t, err, "failed to make install_base writable: %s", string(out))

	ctx := context.Background()

	// Build the bazel startup options (appear before the command).
	bazelStartupOptions := []string{
		"--install_base=" + installBase,
	}

	// Resolve the repository cache path (may be a symlink in runfiles).
	realRepoCache, err := filepath.EvalSymlinks(repoCache)
	require.NoError(t, err, "failed to resolve repository_cache path")

	// Build the bazel args to pass to the prober.
	// The prober will create a temporary workspace with genrule targets and run bazel build.
	bazelArgs := []string{
		"--repository_cache=" + realRepoCache,
		"--remote_executor=" + env.GetRemoteExecutionTarget(),
		"--remote_instance_name=",
		"--remote_default_exec_properties=OSFamily=" + runtime.GOOS,
		"--remote_default_exec_properties=Arch=" + runtime.GOARCH,
		// Use remote strategy since we're testing remote execution and 'sandboxed'
		// isn't available in the test sandbox environment.
		"--spawn_strategy=remote",
	}

	// Run the prober
	cmd := exec.CommandContext(ctx, proberPath,
		"--bazel_binary="+bazelPath,
		"--bazel_startup_options="+strings.Join(bazelStartupOptions, " "),
		"--bazel_args="+strings.Join(bazelArgs, " "),
		"--prober_name=test_prober",
		"--num_targets=2",
		"--num_inputs_per_target=2",
		"--input_size_bytes=1000",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	require.NoError(t, err, "bazelrbe prober should complete successfully")
}
