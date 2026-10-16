package byte_stream_client_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/real_environment"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/byte_stream_client"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testhealthcheck"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	bspb "google.golang.org/genproto/googleapis/bytestream"
)

// countFDs counts this process's open file descriptors. (testleak does not
// check FDs yet, so this test uses a small local helper.)
func countFDs(t *testing.T) int {
	ents, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	return len(ents)
}

// countGRPCGoroutines counts goroutines running gRPC code.
func countGRPCGoroutines() int {
	buf := make([]byte, 1<<24)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, "google.golang.org/grpc") {
			n++
		}
	}
	return n
}

type notFoundByteStreamServer struct {
	bspb.UnimplementedByteStreamServer
}

func (*notFoundByteStreamServer) Read(req *bspb.ReadRequest, stream bspb.ByteStream_ReadServer) error {
	return status.NotFoundError("not found")
}

func unusedAddr(t *testing.T) string {
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	return addr
}

// Every distinct bytestream target (host:port plus userinfo, taken from the
// requested bytestream URL) gets its own cached connection pool. Pools for
// targets that are no longer used must eventually be closed, otherwise the
// app's goroutines and sockets grow with the number of distinct targets it has
// ever been asked to read from.
func TestPoolCache_UnusedPoolsAreClosed(t *testing.T) {
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	bspb.RegisterByteStreamServer(srv, &notFoundByteStreamServer{})
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	liveAddr := lis.Addr().String()

	clock := clockwork.NewFakeClock()
	env := real_environment.NewRealEnv(testhealthcheck.NewTestingHealthChecker())
	env.SetClock(clock)
	c := byte_stream_client.NewPooledByteStreamClient(env)

	read := func(rawURL string) {
		u, err := url.Parse(rawURL)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.Error(t, c.StreamBytestreamFile(ctx, u, io.Discard))
	}
	resource := "/blobs/" + strings.Repeat("0", 64) + "/3"

	baseGoroutines, baseFDs := countGRPCGoroutines(), countFDs(t)

	const numTargets = 20
	for i := range numTargets {
		// Half the targets are unreachable hosts, half are a live host
		// reached with distinct credentials in the URL.
		if i%2 == 0 {
			read("bytestream://" + unusedAddr(t) + resource)
		} else {
			read(fmt.Sprintf("bytestream://user%d@%s%s", i, liveAddr, resource))
		}
	}
	grownGoroutines, grownFDs := countGRPCGoroutines(), countFDs(t)
	t.Logf("after %d distinct targets: grpc goroutines %d -> %d, fds %d -> %d", numTargets, baseGoroutines, grownGoroutines, baseFDs, grownFDs)

	// Let the pools go unused for a long time, then read from one new
	// target.
	clock.Advance(24 * time.Hour)
	read(fmt.Sprintf("bytestream://latest@%s%s", liveAddr, resource))

	// At most one target's pools should remain open: two pools (grpcs and
	// grpc) of two connections each. Allow 40 goroutines and 6 FDs for them.
	maxGoroutines, maxFDs := baseGoroutines+40, baseFDs+6
	var goroutines, fds int
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		goroutines, fds = countGRPCGoroutines(), countFDs(t)
		if goroutines <= maxGoroutines && fds <= maxFDs {
			break
		}
	}
	require.LessOrEqual(t, goroutines, maxGoroutines, "connection pools for unused targets were not closed (grpc goroutines)")
	require.LessOrEqual(t, fds, maxFDs, "connection pools for unused targets were not closed (fds)")
}
