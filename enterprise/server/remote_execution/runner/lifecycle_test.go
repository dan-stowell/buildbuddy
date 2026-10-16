package runner

import (
	"context"
	"os"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// This test has its own target because Check and CheckFDs see everything in
// the process. In runner_test, earlier tests leave commands running (e.g.
// "sleep infinity" and persistent workers), and the stats monitors of those
// commands keep opening pidfds while this test runs. Those pidfds are only
// closed when the GC runs, so they would be reported here.

// Recycling runners through a bounded pool and shutting the pool down must
// remove every workspace and leave no goroutines or FDs behind.
func TestRunnerLifecycleResources(t *testing.T) {
	testleak.Check(t)
	testleak.CheckFDs(t)
	env := testenv.GetTestEnv(t)
	env.SetAuthenticator(testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1")))
	flags.Set(t, "executor.runner_pool.max_runner_count", 2)
	flags.Set(t, "executor.runner_pool.max_runner_disk_size_bytes", -1)
	flags.Set(t, "executor.runner_pool.max_runner_memory_usage_bytes", -1)
	p, err := NewPool(env, testfs.MakeTempDir(t), &PoolOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { p.Shutdown(context.Background()) })
	ctx, err := env.GetAuthenticator().(*testauth.TestAuthenticator).WithAuthenticatedUser(context.Background(), "US1")
	require.NoError(t, err)
	task := &repb.ScheduledTask{ExecutionTask: &repb.ExecutionTask{
		Command: &repb.Command{
			Arguments: []string{"pwd"},
			Platform: &repb.Platform{Properties: []*repb.Platform_Property{
				{Name: "recycle-runner", Value: "true"},
			}},
		},
	}}
	// Keep every runner workspace path so shutdown cleanup is checked directly.
	var paths []string
	for range 11 {
		r, err := p.Get(ctx, task)
		require.NoError(t, err)
		tr := r.(*taskRunner)
		taskCtx, cancel := context.WithCancel(ctx)
		res := tr.Run(taskCtx, &repb.IOStats{})
		cancel()
		require.NoError(t, res.Error)
		paths = append(paths, tr.Workspace.Path())
		p.TryRecycle(ctx, tr, true)
	}
	require.LessOrEqual(t, p.RunnerCount(), 2, "runner cache must stay bounded")
	require.NoError(t, p.Shutdown(ctx))
	p.Wait()
	for _, path := range paths {
		_, err := os.Stat(path)
		require.True(t, os.IsNotExist(err), "workspace remains: %s (%v)", path, err)
	}
}
