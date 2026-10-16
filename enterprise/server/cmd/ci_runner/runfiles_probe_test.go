package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

type probeCAS struct {
	repb.UnimplementedContentAddressableStorageServer
}

func (probeCAS) FindMissingBlobs(ctx context.Context, req *repb.FindMissingBlobsRequest) (*repb.FindMissingBlobsResponse, error) {
	return &repb.FindMissingBlobsResponse{}, nil
}

type countingListener struct {
	net.Listener
	open atomic.Int64
}

type countedConn struct {
	net.Conn
	l    *countingListener
	once atomic.Bool
}

func (c *countedConn) Close() error {
	if c.once.CompareAndSwap(false, true) {
		c.l.open.Add(-1)
	}
	return c.Conn.Close()
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.open.Add(1)
	return &countedConn{Conn: c, l: l}, nil
}

func numFDs() int {
	ents, _ := os.ReadDir("/proc/self/fd")
	return len(ents)
}

func settle(base int, f func() int) int {
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := f()
		if n <= base || time.Now().After(deadline) {
			return n
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestProbeUploadRunfiles(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cl := &countingListener{Listener: lis}
	srv := grpc.NewServer()
	repb.RegisterContentAddressableStorageServer(srv, probeCAS{})
	go srv.Serve(cl)
	defer srv.Stop()
	flags.Set(t, "cache_backend", "grpc://"+lis.Addr().String())

	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "WORKSPACE"), nil, 0644))
	bin := filepath.Join(ws, "bin")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755))
	rf := bin + ".runfiles"
	require.NoError(t, os.MkdirAll(rf, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(rf, "data"), []byte("data"), 0644))

	total := 0
	for _, n := range []int{1, 4, 5} {
		g0, fd0, c0 := runtime.NumGoroutine(), numFDs(), cl.open.Load()
		for range n {
			_, _, _, err := uploadRunfiles(context.Background(), ws, rf, bin)
			require.NoError(t, err)
		}
		total += n
		g := settle(g0, runtime.NumGoroutine)
		fd := settle(fd0, numFDs)
		c := settle(int(c0), func() int { return int(cl.open.Load()) })
		t.Logf("calls=%d (cumulative %d): goroutines +%d, fds +%d, server-side open conns +%d (total %d)", n, total, g-g0, fd-fd0, int64(c)-c0, c)
	}
	fmt.Println("done")
}
