package remote_execution_test

import (
	"context"
	"fmt"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/cachetools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/test/integration/remote_execution/rbetest"
	bespb "github.com/buildbuddy-io/buildbuddy/proto/build_event_stream"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	"github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_handler"
	"github.com/buildbuddy-io/buildbuddy/server/build_event_publisher"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestResourceSoak(t *testing.T) {
	flags.Set(t, "grpc_server_worker_multiplier", 0)
	rbe := rbetest.NewRBETestEnv(t)
	app := rbe.AddBuildBuddyServer()
	rbe.AddExecutor(t)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(root+"/blob", make([]byte, 3<<20), 0600))
	command := func(script string) *repb.Command {
		return &repb.Command{Arguments: []string{"sh", "-c", script}, Platform: &repb.Platform{Properties: []*repb.Platform_Property{{Name: "OSFamily", Value: runtime.GOOS}, {Name: "Arch", Value: runtime.GOARCH}}}, OutputFiles: []string{"out"}}
	}
	stop := make(chan struct{})
	defer close(stop)
	testleak.Soak(t, 2, 10, func(batch int) {
		for i := 0; i < 17; i++ {
			script := "cp blob out"
			exit := 0
			if i%5 == 0 {
				script = "exit 7"
				exit = 7
			}
			res := rbe.Execute(command(script), &rbetest.ExecuteOpts{InputRootDir: root, CheckCache: i%2 == 0}).Wait()
			require.Equal(t, exit, res.ExitCode)
			if exit == 0 {
				for _, out := range res.ActionResult.GetOutputFiles() {
					require.NoError(t, cachetools.GetBlob(context.Background(), rbe.GetByteStreamClient(), digest.NewCASResourceName(out.GetDigest(), res.InstanceName, repb.DigestFunction_SHA256), io.Discard))
				}
			}
			if os.Getenv("SOAK_INJECT_LEAK") == "1" {
				go func() { <-stop }()
			}
		}
		rbe.Execute(command("sleep 30"), &rbetest.ExecuteOpts{InputRootDir: root, ActionTimeout: 100 * time.Millisecond}).MustTerminateAbnormally()
		c := rbe.ExecuteControlledCommand(fmt.Sprintf("disconnect-%d", batch), &rbetest.ExecuteControlledOpts{})
		c.WaitStarted()
		c.WaitAccepted()
		c.ReplaceWaitUsingWaitExecutionAPI()
		c.Exit(0)
		c.Wait()
		c.WaitDisconnected()
		iid := fmt.Sprintf("soak-invocation-%d", batch)
		bep, err := build_event_publisher.New(app.PublishBuildEventClient(), "", iid)
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
