package redisutil_test

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testredis"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/redisutil"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/stretchr/testify/require"
)

func probe(t *testing.T, mode string) {
	h := testredis.StartTCP(t)
	env := testenv.GetTestEnv(t)
	rdb, err := redisutil.NewClientWithOpts(redisutil.TargetToOpts(h.Target), env.GetHealthChecker(), "probe")
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, rdb.Ping(ctx).Err())
	buf := redisutil.NewCommandBuffer(rdb)
	buf.StartPeriodicFlush(context.Background())
	require.NoError(t, buf.IncrBy(ctx, "k", 1))
	time.Sleep(400 * time.Millisecond) // flush once ok

	switch mode {
	case "paused":
		admin := h.Client()
		require.NoError(t, admin.Do(ctx, "CLIENT", "PAUSE", "120000", "ALL").Err())
	case "killed":
		h.Shutdown()
	}
	require.NoError(t, buf.IncrBy(ctx, "k", 1))
	time.Sleep(400 * time.Millisecond) // periodic flush now in progress

	sctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	start := time.Now()
	err = buf.StopPeriodicFlush(sctx)
	t.Logf("mode=%s StopPeriodicFlush(1s deadline) took %s, err=%v", mode, time.Since(start).Round(10*time.Millisecond), err)
}

func TestProbeStopFlushPaused(t *testing.T) { probe(t, "paused") }
func TestProbeStopFlushKilled(t *testing.T) { probe(t, "killed") }

// blackholeAddr returns an address whose accept queue is full, so the
// kernel drops new SYNs and dials hang until they time out.
func blackholeAddr(t *testing.T) string {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	require.NoError(t, err)
	t.Cleanup(func() { syscall.Close(fd) })
	require.NoError(t, syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}))
	require.NoError(t, syscall.Listen(fd, 0))
	sa, err := syscall.Getsockname(fd)
	require.NoError(t, err)
	addr := fmt.Sprintf("127.0.0.1:%d", sa.(*syscall.SockaddrInet4).Port)
	for i := 0; i < 10; i++ {
		c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err != nil {
			t.Logf("backlog full after %d dials: %v", i, err)
			return addr
		}
		t.Cleanup(func() { c.Close() })
	}
	t.Fatal("could not fill backlog")
	return ""
}

func TestProbeStopFlushPartition(t *testing.T) {
	h := testredis.StartTCP(t)
	env := testenv.GetTestEnv(t)
	bh := blackholeAddr(t)
	var partitioned atomic.Bool
	opts := redisutil.TargetToOpts(h.Target)
	orig := opts.Dialer
	opts.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if partitioned.Load() {
			addr = bh
		}
		return orig(ctx, network, addr)
	}
	rdb, err := redisutil.NewClientWithOpts(opts, env.GetHealthChecker(), "probe")
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, rdb.Ping(ctx).Err())
	buf := redisutil.NewCommandBuffer(rdb)
	buf.StartPeriodicFlush(context.Background())
	require.NoError(t, buf.IncrBy(ctx, "k", 1))
	time.Sleep(400 * time.Millisecond)
	require.NoError(t, h.Client().Do(ctx, "CLIENT", "PAUSE", "600000", "ALL").Err())
	partitioned.Store(true)
	require.NoError(t, buf.IncrBy(ctx, "k", 1))
	time.Sleep(400 * time.Millisecond)
	sctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	start := time.Now()
	err = buf.StopPeriodicFlush(sctx)
	t.Logf("mode=partition StopPeriodicFlush(1s deadline) took %s, err=%v", time.Since(start).Round(10*time.Millisecond), err)
}
