package remote_execution_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/test/integration/remote_execution/rbetest"
	bespb "github.com/buildbuddy-io/buildbuddy/proto/build_event_stream"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_handler"
	"github.com/buildbuddy-io/buildbuddy/server/build_event_publisher"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/cachetools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestResourceSoak runs a mixed remote execution workload against one app and
// one executor, and fails if any group of goroutines or file descriptors grows
// steadily over the batches (see testleak.Soak). It takes a few minutes, so it
// is tagged manual:
//
//	bb remote test --config=remote //enterprise/server/test/integration/remote_execution:soak_test --test_output=all --test_arg=-test.v
//
// Each batch runs 17 actions (successful 3 MiB input/output copies and nonzero
// exits, half of them with cache lookups, so that some hit the action cache),
// an action timeout, an Execute stream replaced by WaitExecution, and an
// invocation cancelled through BES. Actions run as bare processes: this does
// not cover OCI/podman/firecracker isolation, VFS, cgroups, or the pebble and
// distributed caches.
//
// To check that the soak catches leaks, --test_env=SOAK_INJECT_LEAK=1 leaks a
// goroutine and a file descriptor per ordinary action, which must fail the
// test with exactly those two groups.
func TestResourceSoak(t *testing.T) {
	// Run server handlers on their own goroutines, so that stuck handlers are
	// counted (see the testleak package docs).
	flags.Set(t, "grpc_server_worker_multiplier", 0)
	rbe := rbetest.NewRBETestEnv(t)
	app := rbe.AddBuildBuddyServer()
	// Each call opens a connection that stays open until the end of the test,
	// so use one for all batches.
	bepClient := app.PublishBuildEventClient()
	rbe.AddExecutor(t)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "blob"), make([]byte, 3<<20), 0600))
	command := func(script string) *repb.Command {
		return &repb.Command{
			Arguments: []string{"sh", "-c", script},
			Platform: &repb.Platform{Properties: []*repb.Platform_Property{
				{Name: "OSFamily", Value: runtime.GOOS},
				{Name: "Arch", Value: runtime.GOARCH},
			}},
			OutputFiles: []string{"out"},
		}
	}
	injectLeak := os.Getenv("SOAK_INJECT_LEAK") == "1"
	stop := make(chan struct{})
	defer close(stop)
	testleak.Soak(t, 2, 10, func(batch int) {
		cacheHits := 0
		for i := range 17 {
			script := "cp blob out"
			exit := 0
			if i%5 == 0 {
				script = "exit 7"
				exit = 7
			}
			cmd := rbe.Execute(command(script), &rbetest.ExecuteOpts{InputRootDir: root, CheckCache: i%2 == 0})
			res := cmd.Wait()
			for _, op := range cmd.GetOperations() {
				if op.GetDone() {
					response := &repb.ExecuteResponse{}
					require.NoError(t, op.GetResponse().UnmarshalTo(response))
					if response.GetCachedResult() {
						cacheHits++
					}
				}
			}
			require.Equal(t, exit, res.ExitCode)
			if exit == 0 {
				for _, out := range res.ActionResult.GetOutputFiles() {
					require.NoError(t, cachetools.GetBlob(context.Background(), rbe.GetByteStreamClient(), digest.NewCASResourceName(out.GetDigest(), res.InstanceName, repb.DigestFunction_SHA256), io.Discard))
				}
			}
			if injectLeak {
				go func() { <-stop }()
				f, err := os.Open("/dev/null")
				require.NoError(t, err)
				t.Cleanup(func() { f.Close() })
			}
		}
		require.Positive(t, cacheHits, "each batch must exercise actual action-cache hits")
		rbe.Execute(command("sleep 30"), &rbetest.ExecuteOpts{InputRootDir: root, ActionTimeout: 100 * time.Millisecond}).MustTerminateAbnormally()
		c := rbe.ExecuteControlledCommand(fmt.Sprintf("disconnect-%d", batch), &rbetest.ExecuteControlledOpts{})
		c.WaitStarted()
		c.WaitAccepted()
		c.ReplaceWaitUsingWaitExecutionAPI()
		c.Exit(0)
		c.Wait()
		c.WaitDisconnected()
		iid := fmt.Sprintf("soak-invocation-%d", batch)
		bep, err := build_event_publisher.New(bepClient, "", iid)
		require.NoError(t, err)
		bep.Start(context.Background())
		require.NoError(t, bep.Publish(&bespb.BuildEvent{Payload: &bespb.BuildEvent_Started{Started: &bespb.BuildStarted{StartTime: timestamppb.Now()}}}))
		c = rbe.ExecuteControlledCommand(fmt.Sprintf("cancel-%d", batch), &rbetest.ExecuteControlledOpts{InvocationID: iid})
		c.WaitStarted()
		require.NoError(t, bep.Publish(&bespb.BuildEvent{Payload: &bespb.BuildEvent_Finished{Finished: &bespb.BuildFinished{ExitCode: &bespb.BuildFinished_ExitCode{Name: "INTERRUPTED", Code: build_event_handler.InterruptedExitCode}, FinishTime: timestamppb.Now()}}}))
		require.NoError(t, bep.Finish())
		c.MustBeCancelled()
		c.WaitDisconnected()
	})
}
