package persistentworker_test

// Exploratory verification tests (V13). Not intended to be merged.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/container"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/containers/bare"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/persistentworker"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/workspace"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

func countFDs() int {
	ents, _ := os.ReadDir("/proc/self/fd")
	return len(ents)
}

func fdTargets() map[string]string {
	ents, _ := os.ReadDir("/proc/self/fd")
	m := map[string]string{}
	for _, e := range ents {
		tgt, _ := os.Readlink("/proc/self/fd/" + e.Name())
		m[e.Name()] = tgt
	}
	return m
}

// settledCounts waits up to d for counts to stop dropping to baseline.
func settledCounts(baseG, baseF int, d time.Duration) (int, int) {
	deadline := time.Now().Add(d)
	for {
		g, f := runtime.NumGoroutine(), countFDs()
		if (g <= baseG && f <= baseF) || time.Now().After(deadline) {
			return g, f
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Worker scripts. Each reads at least one byte of the request first.
const (
	// Emits non-protocol output, then a lot more output, then stays alive.
	scriptGarbageThenMore = `head -c 1 >/dev/null; printf 'garbage\n'; head -c 1048576 /dev/zero; exec sleep 1000`
	// Same, but exits normally after writing.
	scriptGarbageThenExit = `head -c 1 >/dev/null; printf 'garbage\n'; head -c 1048576 /dev/zero; exit 0`
	// proto: valid varint length (1) followed by an invalid proto byte.
	scriptProtoBadThenMore = `head -c 1 >/dev/null; printf '\001\377'; head -c 1048576 /dev/zero; exec sleep 1000`
	// Garbage only, no further output.
	scriptGarbageOnly = `head -c 1 >/dev/null; printf 'garbage\n'; exec sleep 1000`
	// Partial response then hang (for ctx-cancel probe).
	scriptPartialProto = `head -c 1 >/dev/null; printf '\144abcdefghij'; exec sleep 1000`
	scriptPartialJSON  = `head -c 1 >/dev/null; printf '{"exitCode": '; exec sleep 1000`
	scriptNoResponse   = `head -c 1 >/dev/null; exec sleep 1000`
)

func startBare(t *testing.T, protocol, script string) (*persistentworker.Worker, *repb.Command) {
	ctr := bare.NewBareCommandContainer(&bare.Opts{})
	require.NoError(t, ctr.Create(context.Background(), t.TempDir()))
	cmd := &repb.Command{Arguments: []string{"sh", "-c", script}}
	w, err := persistentworker.Start(context.Background(), &workspace.Workspace{}, ctr, protocol, cmd)
	require.NoError(t, err)
	return w, cmd
}

// ociLikeContainer mimics ociContainer.invokeRuntime's stdio wiring for
// persistent workers: exec.CommandContext + cmd.Stdin/Stdout set directly +
// WaitDelay=1us.
type ociLikeContainer struct {
	container.CommandContainer
}

func (c *ociLikeContainer) Exec(ctx context.Context, command *repb.Command, stdio *interfaces.Stdio) *interfaces.CommandResult {
	cmd := exec.CommandContext(ctx, command.GetArguments()[0], command.GetArguments()[1:]...)
	cmd.Stdin = stdio.Stdin
	cmd.Stdout = stdio.Stdout
	cmd.Stderr = stdio.Stderr
	cmd.WaitDelay = 1 * time.Microsecond
	err := cmd.Run()
	return &interfaces.CommandResult{Error: err, ExitCode: cmd.ProcessState.ExitCode()}
}

func runStopScenario(t *testing.T, n int, start func() *persistentworker.Worker, execCmd *repb.Command) {
	baseG, baseF := runtime.NumGoroutine(), countFDs()
	baseFDs := fdTargets()
	workers := make([]*persistentworker.Worker, n)
	for i := range n {
		w := start()
		workers[i] = w
		res := w.Exec(context.Background(), execCmd)
		require.Error(t, res.Error)
		if i == 0 {
			t.Logf("exec error: %.120s", res.Error)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	stopErrs := 0
	var maxDur time.Duration
	for _, w := range workers {
		wg.Go(func() {
			st := time.Now()
			err := w.Stop()
			d := time.Since(st)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				stopErrs++
			}
			maxDur = max(maxDur, d)
		})
	}
	wg.Wait()
	g, f := settledCounts(baseG, baseF, 3*time.Second)
	t.Logf("RESULT N=%d stopErrors=%d maxStop=%s goroutineDelta=%d fdDelta=%d", n, stopErrs, maxDur.Round(time.Millisecond), g-baseG, f-baseF)
	if f-baseF > 0 {
		after := fdTargets()
		shown := 0
		for k, v := range after {
			if baseFDs[k] != v && shown < 4 {
				t.Logf("  new fd %s -> %s", k, v)
				shown++
			}
		}
	}
	if g-baseG > 0 && n == 10 {
		buf := make([]byte, 1<<22)
		buf = buf[:runtime.Stack(buf, true)]
		counts := map[string]int{}
		for gs := range strings.SplitSeq(string(buf), "\n\n") {
			lines := strings.Split(gs, "\n")
			if len(lines) < 2 || !(strings.Contains(gs, "os/exec") || strings.Contains(gs, "persistentworker.Start")) {
				continue
			}
			var fns []string
			for i := 1; i < len(lines); i += 2 {
				fn := lines[i]
				if j := strings.LastIndex(fn, "("); j > 0 {
					fn = fn[:j]
				}
				fns = append(fns, fn)
			}
			counts[strings.Join(fns, " <- ")]++
		}
		for k, v := range counts {
			t.Logf("  STACK x%d: %s", v, k)
		}
	}
}

func TestVerifyStopAfterMalformed(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, script string
		ociLike                bool
	}{
		{"bare_json_garbage_then_more", "json", scriptGarbageThenMore, false},
		{"bare_json_garbage_then_exit", "json", scriptGarbageThenExit, false},
		{"bare_proto_bad_then_more", "proto", scriptProtoBadThenMore, false},
		{"bare_json_garbage_only", "json", scriptGarbageOnly, false},
		{"ocilike_json_garbage_then_more", "json", scriptGarbageThenMore, true},
	} {
		for _, n := range []int{1, 10, 50} {
			t.Run(fmt.Sprintf("%s/N=%d", tc.name, n), func(t *testing.T) {
				cmd := &repb.Command{Arguments: []string{"sh", "-c", tc.script}}
				start := func() *persistentworker.Worker {
					if tc.ociLike {
						w, err := persistentworker.Start(context.Background(), &workspace.Workspace{}, &ociLikeContainer{}, tc.protocol, cmd)
						require.NoError(t, err)
						return w
					}
					w, _ := startBare(t, tc.protocol, tc.script)
					return w
				}
				runStopScenario(t, n, start, cmd)
			})
		}
	}
}

// Probe: request ctx cancelled (action timeout) while the worker is blocked
// mid-response.
func TestVerifyCtxCancelMidResponse(t *testing.T) {
	for _, tc := range []struct{ name, protocol, script string }{
		{"proto_partial", "proto", scriptPartialProto},
		{"json_partial", "json", scriptPartialJSON},
		{"proto_no_response", "proto", scriptNoResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseG, baseF := runtime.NumGoroutine(), countFDs()
			w, cmd := startBare(t, tc.protocol, tc.script)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			done := make(chan *interfaces.CommandResult, 1)
			st := time.Now()
			go func() { done <- w.Exec(ctx, cmd) }()
			var res *interfaces.CommandResult
			select {
			case res = <-done:
				t.Logf("RESULT %s: Exec returned after %s (ctx deadline 500ms): %v", tc.name, time.Since(st).Round(time.Millisecond), res.Error)
			case <-time.After(5 * time.Second):
				t.Logf("RESULT %s: Exec STILL BLOCKED 5s after start (ctx expired at 500ms)", tc.name)
			}
			st = time.Now()
			err := w.Stop()
			t.Logf("RESULT %s: Stop err=%v after %s", tc.name, err, time.Since(st).Round(time.Millisecond))
			if res == nil {
				select {
				case res = <-done:
					t.Logf("RESULT %s: Exec returned after Stop: %v", tc.name, res.Error)
				case <-time.After(5 * time.Second):
					t.Logf("RESULT %s: Exec still blocked after Stop", tc.name)
				}
			}
			g, f := settledCounts(baseG, baseF, 3*time.Second)
			t.Logf("RESULT %s: goroutineDelta=%d fdDelta=%d", tc.name, g-baseG, f-baseF)
		})
	}
}

// Fix review: Stop while an Exec is blocked reading the response.
func TestVerifyFixStopDuringInFlightRead(t *testing.T) {
	baseG, baseF := runtime.NumGoroutine(), countFDs()
	for _, protocol := range []string{"json", "proto"} {
		w, cmd := startBare(t, protocol, scriptNoResponse)
		done := make(chan *interfaces.CommandResult, 1)
		go func() { done <- w.Exec(context.Background(), cmd) }()
		time.Sleep(200 * time.Millisecond)
		require.NoError(t, w.Stop())
		select {
		case res := <-done:
			require.Error(t, res.Error)
			t.Logf("RESULT inflight %s: Exec error after Stop: %.160s", protocol, res.Error)
		case <-time.After(5 * time.Second):
			t.Fatalf("Exec still blocked after Stop")
		}
	}
	g, f := settledCounts(baseG, baseF, 3*time.Second)
	t.Logf("RESULT inflight: goroutineDelta=%d fdDelta=%d", g-baseG, f-baseF)
	require.LessOrEqual(t, g-baseG, 0)
	require.LessOrEqual(t, f-baseF, 0)
}

// Fix review: Stop twice, Stop after normal exit, healthy reuse.
func TestVerifyFixStopEdgeCases(t *testing.T) {
	baseG, baseF := runtime.NumGoroutine(), countFDs()
	// Stop twice after malformed output.
	w, cmd := startBare(t, "json", scriptGarbageThenMore)
	require.Error(t, w.Exec(context.Background(), cmd).Error)
	require.NoError(t, w.Stop())
	require.NoError(t, w.Stop())

	// Worker responds, then exits normally before Stop.
	w, cmd = startBare(t, "json", `head -c 1 >/dev/null; printf '{"exitCode": 0, "output": "ok"}\n'; exit 0`)
	res := w.Exec(context.Background(), cmd)
	require.NoError(t, res.Error)
	require.Equal(t, "ok", string(res.Stderr))
	time.Sleep(200 * time.Millisecond)
	require.NoError(t, w.Stop())

	// Healthy reuse: 10 requests on one worker, then Stop.
	w, cmd = startBare(t, "json", `while read -r line; do printf '{"exitCode": 0, "output": "ok"}\n'; done`)
	for range 10 {
		res := w.Exec(context.Background(), cmd)
		require.NoError(t, res.Error)
		require.Equal(t, "ok", string(res.Stderr))
	}
	require.NoError(t, w.Stop())

	g, f := settledCounts(baseG, baseF, 3*time.Second)
	t.Logf("RESULT edge: goroutineDelta=%d fdDelta=%d", g-baseG, f-baseF)
	require.LessOrEqual(t, g-baseG, 0)
	require.LessOrEqual(t, f-baseF, 0)
}
