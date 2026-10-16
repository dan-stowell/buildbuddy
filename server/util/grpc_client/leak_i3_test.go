package grpc_client_test

import (
	"context"
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/util/grpc_client"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pspb "github.com/buildbuddy-io/buildbuddy/proto/ping_service"
)

// Local FD helper (testleak does not check FDs yet).
func i3CountFDs(t *testing.T) int {
	ents, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	return len(ents)
}

func i3FDTargets(t *testing.T) map[string]int {
	out := map[string]int{}
	ents, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	for _, e := range ents {
		tgt, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err != nil {
			continue
		}
		if len(tgt) > 7 && tgt[:7] == "socket:" {
			tgt = "socket"
		}
		out[tgt]++
	}
	return out
}

// i3Settle polls until goroutines and FDs are <= the given limits, or the
// deadline passes, and returns the last observed counts.
func i3Settle(t *testing.T, maxG, maxFD int) (int, int) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		runtime.GC()
		g, fd := runtime.NumGoroutine(), i3CountFDs(t)
		if (g <= maxG && fd <= maxFD) || time.Now().After(deadline) {
			return g, fd
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// i3Quiesce polls until goroutine and FD counts are stable for a few samples.
func i3Quiesce(t *testing.T) (int, int) {
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
	return lastG, lastFD
}

// i3Warmup dials and closes a pool once so that process-wide lazily-started
// goroutines (if any) are not attributed to the measured operations.
func i3Warmup(t *testing.T, target string) {
	pool, err := grpc_client.DialSimpleWithPoolSize(target, 2)
	require.NoError(t, err)
	_ = i3Ping(context.Background(), pspb.NewApiClient(pool), 0)
	require.NoError(t, pool.Close())
}

type i3PingServer struct{}

func (i3PingServer) Ping(ctx context.Context, req *pspb.PingRequest) (*pspb.PingResponse, error) {
	if req.GetTag()%2 == 1 {
		return nil, status.UnavailableError("odd tag")
	}
	return &pspb.PingResponse{Tag: req.GetTag()}, nil
}

type i3Server struct {
	addr string
	srv  *grpc.Server
}

func i3StartServer(t *testing.T, addr string) *i3Server {
	var lis net.Listener
	var err error
	for range 100 {
		lis, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, err)
	s := grpc.NewServer()
	pspb.RegisterApiServer(s, i3PingServer{})
	go s.Serve(lis)
	return &i3Server{addr: lis.Addr().String(), srv: s}
}

func i3Ping(ctx context.Context, c pspb.ApiClient, tag int64) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := c.Ping(ctx, &pspb.PingRequest{Tag: uint64(tag)})
	return err
}

func TestI3_PoolDialCloseRepeat(t *testing.T) {
	ctx := context.Background()
	s := i3StartServer(t, "localhost:0")
	defer s.srv.Stop()
	target := "grpc://" + s.addr
	i3Warmup(t, target)
	g0, fd0 := i3Quiesce(t)
	t.Logf("baseline goroutines=%d fds=%d", g0, fd0)
	total := 0
	for _, n := range []int{1, 10, 50} {
		for range n {
			pool, err := grpc_client.DialSimpleWithPoolSize(target, 10)
			require.NoError(t, err)
			c := pspb.NewApiClient(pool)
			for j := range 20 {
				_ = i3Ping(ctx, c, int64(j))
			}
			require.NoError(t, pool.Close())
		}
		total += n
		g, fd := i3Settle(t, g0, fd0)
		t.Logf("after %d dial/close cycles (pool size 10): goroutines=%d (delta %d) fds=%d (delta %d)", total, g, g-g0, fd, fd-fd0)
	}
	g, fd := i3Settle(t, g0, fd0)
	require.LessOrEqual(t, g, g0)
	require.LessOrEqual(t, fd, fd0, "fd targets: %v", i3FDTargets(t))
}

func TestI3_PoolRPCVolume(t *testing.T) {
	ctx := context.Background()
	s := i3StartServer(t, "localhost:0")
	defer s.srv.Stop()
	i3Warmup(t, "grpc://"+s.addr)
	g0, fd0 := i3Quiesce(t)
	pool, err := grpc_client.DialSimpleWithPoolSize("grpc://"+s.addr, 10)
	require.NoError(t, err)
	c := pspb.NewApiClient(pool)
	done := 0
	var gAfter100 int
	for _, n := range []int{100, 1000, 5000} {
		for done < n {
			_ = i3Ping(ctx, c, int64(done)) // half return errors
			// some RPCs with an already-cancelled context
			if done%10 == 0 {
				cctx, cancel := context.WithCancel(ctx)
				cancel()
				_, _ = c.Ping(cctx, &pspb.PingRequest{})
			}
			done++
		}
		g, fd := i3Quiesce(t)
		if n == 100 {
			gAfter100 = g
		}
		t.Logf("pool alive after %d RPCs: goroutines=%d (delta %d) fds=%d (delta %d)", n, g, g-g0, fd, fd-fd0)
	}
	g, _ := i3Settle(t, gAfter100, 0)
	require.LessOrEqual(t, g, gAfter100+2)
	require.NoError(t, pool.Close())
	g, fd := i3Settle(t, g0, fd0)
	t.Logf("after close: goroutines=%d (delta %d) fds=%d (delta %d)", g, g-g0, fd, fd-fd0)
	require.LessOrEqual(t, g, g0)
	require.LessOrEqual(t, fd, fd0)
}

func TestI3_PoolServerRestarts(t *testing.T) {
	ctx := context.Background()
	s := i3StartServer(t, "localhost:0")
	addr := s.addr
	i3Warmup(t, "grpc://"+addr)
	g0, fd0 := i3Quiesce(t)
	pool, err := grpc_client.DialSimpleWithPoolSize("grpc://"+addr, 10)
	require.NoError(t, err)
	c := pspb.NewApiClient(pool)
	for j := range 50 {
		_ = i3Ping(ctx, c, int64(j*2))
	}
	gAlive, fdAlive := i3Quiesce(t)
	t.Logf("pool alive, server up, before restarts: goroutines=%d fds=%d", gAlive, fdAlive)
	restarts := 0
	for _, n := range []int{1, 5, 20} {
		for restarts < n {
			s.srv.Stop()
			// RPCs while server down (fail fast / unavailable)
			for range 20 {
				_ = i3Ping(ctx, c, 0)
			}
			s = i3StartServer(t, addr)
			// wait for pool to recover on all conns
			ok := 0
			for range 400 {
				if ok >= 40 {
					break
				}
				rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				_, err := c.Ping(rctx, &pspb.PingRequest{}, grpc.WaitForReady(true))
				cancel()
				if err == nil {
					ok++
				}
			}
			require.Equal(t, 40, ok, "pool did not recover after restart")
			restarts++
		}
		g, fd := i3Quiesce(t)
		t.Logf("after %d server restarts: goroutines=%d (delta vs alive %d) fds=%d (delta vs alive %d)", restarts, g, g-gAlive, fd, fd-fdAlive)
	}
	require.NoError(t, pool.Close())
	s.srv.Stop()
	g, fd := i3Settle(t, g0, fd0)
	t.Logf("after close: goroutines=%d (delta %d) fds=%d (delta %d)", g, g-g0, fd, fd-fd0)
	require.LessOrEqual(t, g, g0)
	require.LessOrEqual(t, fd, fd0)
}

func TestI3_PoolUnreachable(t *testing.T) {
	ctx := context.Background()
	// Grab a free port and close it so nothing listens.
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	lis.Close()
	s := i3StartServer(t, "localhost:0")
	i3Warmup(t, "grpc://"+s.addr)
	s.srv.Stop()
	g0, fd0 := i3Quiesce(t)
	for _, n := range []int{1, 10, 30} {
		pool, err := grpc_client.DialSimpleWithPoolSize("grpc://"+addr, 10)
		require.NoError(t, err)
		c := pspb.NewApiClient(pool)
		for j := range n {
			cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			_, _ = c.Ping(cctx, &pspb.PingRequest{}, grpc.WaitForReady(j%2 == 0))
			cancel()
		}
		g, fd := runtime.NumGoroutine(), i3CountFDs(t)
		t.Logf("unreachable target, pool alive after %d failing RPCs: goroutines=%d (delta %d) fds=%d (delta %d)", n, g, g-g0, fd, fd-fd0)
		require.NoError(t, pool.Close())
		g, fd = i3Settle(t, g0, fd0)
		t.Logf("  after close: goroutines=%d (delta %d) fds=%d (delta %d)", g, g-g0, fd, fd-fd0)
		require.LessOrEqual(t, g, g0)
		require.LessOrEqual(t, fd, fd0)
	}
}
