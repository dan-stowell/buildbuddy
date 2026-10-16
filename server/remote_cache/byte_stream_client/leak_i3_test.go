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
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	bspb "google.golang.org/genproto/googleapis/bytestream"
)

// Local FD helper (testleak does not check FDs yet).
func i3CountFDs(t *testing.T) int {
	ents, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	return len(ents)
}

func i3GrpcGoroutines() int {
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

// i3Quiesce polls until goroutine and FD counts are stable for a few samples.
func i3Quiesce(t *testing.T) (int, int, int) {
	lastG, lastFD, stable := -1, -1, 0
	for i := 0; i < 100 && stable < 5; i++ {
		runtime.GC()
		g, fd := runtime.NumGoroutine(), i3CountFDs(t)
		if g == lastG && fd == lastFD {
			stable++
		} else {
			stable = 0
		}
		lastG, lastFD = g, fd
		time.Sleep(50 * time.Millisecond)
	}
	return lastG, lastFD, i3GrpcGoroutines()
}

type i3BSServer struct {
	bspb.UnimplementedByteStreamServer
}

func (*i3BSServer) Read(req *bspb.ReadRequest, stream bspb.ByteStream_ReadServer) error {
	return status.NotFoundError("not found")
}

func i3FreePort(t *testing.T) string {
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	lis.Close()
	return addr
}

const i3Resource = "/blobs/" + "0000000000000000000000000000000000000000000000000000000000000000" + "/3"

// With the pool cache enabled (the default), every distinct bytestream target
// gets a connection pool that is cached forever: there is no eviction and no
// Close. Measures goroutines/FDs vs the number of distinct targets requested.
func TestI3_PooledByteStreamClient_DistinctTargets(t *testing.T) {
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	bspb.RegisterByteStreamServer(srv, &i3BSServer{})
	go srv.Serve(lis)
	defer srv.Stop()
	liveAddr := lis.Addr().String()

	for _, variant := range []string{"unreachable-hosts", "live-host-distinct-userinfo"} {
		t.Run(variant, func(t *testing.T) {
			env := real_environment.NewRealEnv(testhealthcheck.NewTestingHealthChecker())
			c := byte_stream_client.NewPooledByteStreamClient(env)
			g0, fd0, gr0 := i3Quiesce(t)
			t.Logf("%s baseline: goroutines=%d fds=%d grpc-goroutines=%d", variant, g0, fd0, gr0)
			done := 0
			for _, n := range []int{1, 10, 50} {
				for done < n {
					var u *url.URL
					if variant == "unreachable-hosts" {
						u, err = url.Parse("bytestream://" + i3FreePort(t) + i3Resource)
					} else {
						u, err = url.Parse(fmt.Sprintf("bytestream://user%d@%s%s", done, liveAddr, i3Resource))
					}
					require.NoError(t, err)
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					err := c.StreamBytestreamFile(ctx, u, io.Discard)
					cancel()
					require.Error(t, err)
					done++
				}
				g, fd, gr := i3Quiesce(t)
				t.Logf("%s after %d distinct targets: goroutines=%d (delta %d) fds=%d (delta %d) grpc-goroutines=%d (delta %d)",
					variant, n, g, g-g0, fd, fd-fd0, gr, gr-gr0)
			}
		})
	}
}
