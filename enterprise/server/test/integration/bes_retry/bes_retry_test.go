package bes_retry_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/buildbuddy_enterprise"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testbazel"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bbspb "github.com/buildbuddy-io/buildbuddy/proto/buildbuddy_service"
	inpb "github.com/buildbuddy-io/buildbuddy/proto/invocation"
	inspb "github.com/buildbuddy-io/buildbuddy/proto/invocation_status"
)

// a1..a3 are cached by a warm-up build. "slow" depends on them (so their AC
// lookups and CAS downloads happen before it runs), touches a marker file so
// the test knows the pre-disconnect cache traffic is done, then sleeps. Its
// action key depends on MARKER so it is always a cache miss. "post" depends
// only on slow's output content (identical across builds), so it is an AC
// hit that happens after the BES stream has been retried.
var workspaceContents = map[string]string{
	"BUILD": `
genrule(name = "a1", outs = ["a1.txt"], cmd_bash = "echo a1 > $@")
genrule(name = "a2", outs = ["a2.txt"], cmd_bash = "echo a2 > $@")
genrule(name = "a3", outs = ["a3.txt"], cmd_bash = "echo a3 > $@")
genrule(
    name = "slow",
    srcs = ["a1.txt", "a2.txt", "a3.txt"],
    outs = ["slow.txt"],
    cmd_bash = "if [ '$(MARKER)' != none ]; then touch '$(MARKER)'; sleep $(SLEEP); fi; cat $(SRCS) > $@",
)
genrule(name = "post", srcs = ["slow.txt"], outs = ["post.txt"], cmd_bash = "cat $< > $@")
`,
}

// cutProxy is a TCP proxy in front of the app's gRPC port, used only as the
// --bes_backend. cut() resets every open connection, which is what Bazel
// sees when an app replica goes away / an LB resets the connection.
type cutProxy struct {
	t      *testing.T
	lis    net.Listener
	target string

	mu       sync.Mutex
	conns    []net.Conn
	accepts  []time.Time
	firstRxs []time.Time
}

func startProxy(t *testing.T, target string) *cutProxy {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &cutProxy{t: t, lis: lis, target: target}
	t.Cleanup(func() { lis.Close(); p.cut() })
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			now := time.Now()
			u, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			p.mu.Lock()
			idx := len(p.accepts)
			p.accepts = append(p.accepts, now)
			p.firstRxs = append(p.firstRxs, time.Time{})
			p.conns = append(p.conns, c, u)
			p.mu.Unlock()
			go func() {
				buf := make([]byte, 32*1024)
				first := true
				for {
					n, err := c.Read(buf)
					if n > 0 {
						if first {
							p.mu.Lock()
							p.firstRxs[idx] = time.Now()
							p.mu.Unlock()
							first = false
						}
						if _, werr := u.Write(buf[:n]); werr != nil {
							break
						}
					}
					if err != nil {
						break
					}
				}
				c.Close()
				u.Close()
			}()
			go func() {
				io.Copy(c, u)
				c.Close()
				u.Close()
			}()
		}
	}()
	return p
}

func (p *cutProxy) addr() string { return p.lis.Addr().String() }

func (p *cutProxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		if tc, ok := c.(*net.TCPConn); ok {
			tc.SetLinger(0) // RST
		}
		c.Close()
	}
	p.conns = nil
}

func (p *cutProxy) snapshot() ([]time.Time, []time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Time{}, p.accepts...), append([]time.Time{}, p.firstRxs...)
}

type buildOutcome struct {
	inv          *inpb.Invocation
	bazelHitLine string
	cutAt        time.Time
	accepts      []time.Time
	firstRxs     []time.Time
	stderr       string
}

var remoteHitRE = regexp.MustCompile(`\d+ remote cache hit`)

func runBuild(t *testing.T, ctx context.Context, ws string, app interface {
	GRPCAddress() string
	RemoteCacheBazelFlags() []string
}, bb bbspb.BuildBuddyServiceClient, grpcHostPort string, doCut bool) *buildOutcome {
	testbazel.Clean(ctx, t, ws)
	proxy := startProxy(t, grpcHostPort)
	iid := uuid.NewString()
	marker := filepath.Join(testfs.MakeTempDir(t), "marker")
	flags := []string{
		"//:post",
		"--invocation_id=" + iid,
		"--bes_backend=grpc://" + proxy.addr(),
		"--spawn_strategy=local",
		"--define=MARKER=" + marker,
		"--define=SLEEP=8",
	}
	flags = append(flags, app.RemoteCacheBazelFlags()...)

	type res struct {
		stderr string
		err    error
	}
	done := make(chan res, 1)
	go func() {
		r := testbazel.Invoke(ctx, t, ws, "build", flags...)
		done <- res{r.Stderr, r.Error}
	}()

	out := &buildOutcome{}
	if doCut {
		// Wait until all pre-disconnect cache traffic is done (slow started).
		deadline := time.Now().Add(120 * time.Second)
		for {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			require.True(t, time.Now().Before(deadline), "slow genrule never started")
			time.Sleep(20 * time.Millisecond)
		}
		// Give the server a moment to have the BES stream open (it is
		// opened at build start, long before this).
		out.cutAt = time.Now()
		proxy.cut()
		t.Logf("cut BES connection(s) at %s", out.cutAt.Format(time.RFC3339Nano))
	}
	r := <-done
	require.NoError(t, r.err, "bazel stderr:\n%s", r.stderr)
	out.stderr = r.stderr
	out.bazelHitLine = remoteHitRE.FindString(r.stderr)
	out.accepts, out.firstRxs = proxy.snapshot()

	// Wait for the final attempt to be COMPLETE, then let the stats recorder
	// (500ms default finalization delay) settle.
	deadline := time.Now().Add(30 * time.Second)
	for {
		rsp, err := bb.GetInvocation(ctx, &inpb.GetInvocationRequest{Lookup: &inpb.InvocationLookup{InvocationId: iid}})
		if err == nil && len(rsp.GetInvocation()) == 1 && rsp.GetInvocation()[0].GetInvocationStatus() == inspb.InvocationStatus_COMPLETE_INVOCATION_STATUS {
			break
		}
		require.True(t, time.Now().Before(deadline), "invocation never completed: %v %v", rsp, err)
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(3 * time.Second)
	rsp, err := bb.GetInvocation(ctx, &inpb.GetInvocationRequest{Lookup: &inpb.InvocationLookup{InvocationId: iid}})
	require.NoError(t, err)
	out.inv = rsp.GetInvocation()[0]
	return out
}

func summarize(o *buildOutcome) string {
	cs := o.inv.GetCacheStats()
	return fmt.Sprintf("attempt=%d status=%s AC{hits=%d misses=%d uploads=%d} CAS{hits=%d misses=%d uploads=%d} downloadBytes=%d uploadBytes=%d scorecardMisses=%d bazel=%q",
		o.inv.GetAttempt(), o.inv.GetInvocationStatus(),
		cs.GetActionCacheHits(), cs.GetActionCacheMisses(), cs.GetActionCacheUploads(),
		cs.GetCasCacheHits(), cs.GetCasCacheMisses(), cs.GetCasCacheUploads(),
		cs.GetTotalDownloadSizeBytes(), cs.GetTotalUploadSizeBytes(),
		len(o.inv.GetScoreCard().GetMisses()), o.bazelHitLine)
}

// TestRealBazelBESDisconnectRetry_CacheStats runs a real Bazel build against
// a real BuildBuddy enterprise app (redis-backed hit tracking, default
// cache_stats_finalization_delay and redis buffering), with the BES stream
// going through a TCP proxy. In the "cut" build, the proxy resets the BES
// connection mid-build after all pre-disconnect cache traffic is done; Bazel
// retries the BES upload on its own. The final invocation's cache stats must
// match a control build that was not cut.
func TestRealBazelBESDisconnectRetry_CacheStats(t *testing.T) {
	testScenario(t, testbazel.MakeTempModule(t, workspaceContents))
}

// Same, but with Bazel's BES retry backoff lowered to 50ms (well below the
// 500ms cache_stats_finalization_delay), to check whether retry timing
// matters.
func TestRealBazelBESDisconnectRetry_FastRetry_CacheStats(t *testing.T) {
	contents := map[string]string{
		".bazelrc": "startup --host_jvm_args=-DBAZEL_BES_MIN_RETRY_SLEEP_MILLIS=50\n",
	}
	for k, v := range workspaceContents {
		contents[k] = v
	}
	testScenario(t, testbazel.MakeTempModule(t, contents))
}

func testScenario(t *testing.T, ws string) {
	app := buildbuddy_enterprise.RunWithConfig(t, buildbuddy_enterprise.DefaultAppConfig(t), buildbuddy_enterprise.NoAuthConfig)
	bb := app.BuildBuddyServiceClient(t)
	ctx := context.Background()
	grpcHostPort := strings.TrimPrefix(app.GRPCAddress(), "grpc://")
	t.Logf("test bazel version: %s", testbazel.Version)

	// Warm-up: populate the remote cache for a1..a3 and post.
	warm := []string{"//:post", "--define=MARKER=none", "--define=SLEEP=0", "--invocation_id=" + uuid.NewString()}
	warm = append(warm, app.RemoteCacheBazelFlags()...)
	r := testbazel.Invoke(ctx, t, ws, "build", warm...)
	require.NoError(t, r.Error, r.Stderr)

	control := runBuild(t, ctx, ws, app, bb, grpcHostPort, false)
	t.Logf("CONTROL: %s", summarize(control))
	cut := runBuild(t, ctx, ws, app, bb, grpcHostPort, true)
	t.Logf("CUT:     %s", summarize(cut))

	for i, a := range cut.accepts {
		line := fmt.Sprintf("BES conn %d accepted at %s", i, a.Format(time.RFC3339Nano))
		if !cut.firstRxs[i].IsZero() {
			line += fmt.Sprintf(", first bytes at %s", cut.firstRxs[i].Format(time.RFC3339Nano))
		}
		if a.After(cut.cutAt) {
			line += fmt.Sprintf(" (cut -> reconnect: %s)", a.Sub(cut.cutAt))
		}
		t.Log(line)
	}
	for l := range strings.SplitSeq(cut.stderr, "\n") {
		if strings.Contains(strings.ToLower(l), "build event") || strings.Contains(l, "BES") || strings.Contains(strings.ToLower(l), "retry") {
			t.Logf("bazel: %s", l)
		}
	}

	// Sanity: the scenario actually happened.
	require.GreaterOrEqual(t, len(cut.accepts), 2, "bazel never reconnected to BES")
	require.Equal(t, control.bazelHitLine, cut.bazelHitLine, "bazel saw the same cache hits in both builds")
	require.GreaterOrEqual(t, cut.inv.GetAttempt(), uint64(2), "cut build should have been retried as a new attempt")
	require.Equal(t, uint64(1), control.inv.GetAttempt())

	ccs, xcs := control.inv.GetCacheStats(), cut.inv.GetCacheStats()
	assert.Equal(t, ccs.GetActionCacheHits(), xcs.GetActionCacheHits(), "AC hits")
	assert.Equal(t, ccs.GetActionCacheMisses(), xcs.GetActionCacheMisses(), "AC misses")
	assert.Equal(t, ccs.GetCasCacheHits(), xcs.GetCasCacheHits(), "CAS hits")
	assert.Equal(t, len(control.inv.GetScoreCard().GetMisses()), len(cut.inv.GetScoreCard().GetMisses()), "scorecard misses")
}
