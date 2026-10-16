package procstats_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/procstats"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/stretchr/testify/require"
)

func pidfdCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	n := 0
	for _, e := range entries {
		target, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err == nil && strings.Contains(target, "pidfd") {
			n++
		}
	}
	return n
}

func TestTreeStatsPidfdGrowth(t *testing.T) {
	for _, disableGC := range []bool{false, true} {
		for _, children := range []int{10, 50} {
			t.Run(fmt.Sprintf("disableGC=%t/children=%d", disableGC, children), func(t *testing.T) {
				// A shell with long-lived children exercises the real process-tree walk.
				cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("i=0; while [ $i -lt %d ]; do sleep 300 & i=$((i+1)); done; echo ready; wait", children))
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				stdout, err := cmd.StdoutPipe()
				require.NoError(t, err)
				require.NoError(t, cmd.Start())
				t.Cleanup(func() {
					require.NoError(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL))
					_ = cmd.Wait()
					_ = stdout.Close()
				})
				line, err := bufio.NewReader(stdout).ReadString('\n')
				require.NoError(t, err)
				require.Equal(t, "ready\n", line)
				runtime.GC()
				if disableGC {
					old := debug.SetGCPercent(-1)
					defer debug.SetGCPercent(old)
				}
				defer runtime.GC()
				stats := procstats.NewTreeStats(cmd.Process.Pid)
				base := pidfdCount(t)
				for n := 1; n <= 50; n++ {
					require.NoError(t, stats.Update())
					require.Len(t, stats.CPUNanosByPid, children+1)
					require.Positive(t, stats.Total().GetMemoryBytes())
					if n == 1 || n == 10 || n == 50 {
						t.Logf("children=%d polls=%d pidfd_delta=%d GC_disabled=%t Go=%s", children, n, pidfdCount(t)-base, disableGC, runtime.Version())
					}
				}
				if disableGC {
					// Keep the tree and sampler alive while checking per-poll growth.
					testleak.CheckGrowth(t, testleak.Growth{Iterations: 10}, func(int) {
						require.NoError(t, stats.Update())
					})
				}
			})
		}
	}
}
