package distributed_client_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/backends/pebble_cache"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testdigest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testport"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"

	rspb "github.com/buildbuddy-io/buildbuddy/proto/resource"
)

// v5CountingCache counts readers opened on the peer that are not yet closed.
type v5CountingCache struct {
	interfaces.Cache
	open atomic.Int64
}

type v5CountedReader struct {
	io.ReadCloser
	c    *v5CountingCache
	once sync.Once
}

func (r *v5CountedReader) Close() error {
	r.once.Do(func() { r.c.open.Add(-1) })
	return r.ReadCloser.Close()
}

func (c *v5CountingCache) Reader(ctx context.Context, rn *rspb.ResourceName, offset, limit int64) (io.ReadCloser, error) {
	rc, err := c.Cache.Reader(ctx, rn, offset, limit)
	if err != nil {
		return nil, err
	}
	c.open.Add(1)
	return &v5CountedReader{ReadCloser: rc, c: c}, nil
}

// v5FDsUnder returns the number of open FDs of this process whose target is
// under dir.
func v5FDsUnder(dir string) int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range ents {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(target, dir) {
			n++
		}
	}
	return n
}

func v5Goroutines(substr string) int {
	buf := make([]byte, 4<<20)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, substr) {
			n++
		}
	}
	return n
}

type v5Counts struct {
	fds, openReaders, serverHandlers, clientStreams int
}

func v5Measure(blobDir string, cc *v5CountingCache) v5Counts {
	return v5Counts{
		fds:            v5FDsUnder(blobDir),
		openReaders:    int(cc.open.Load()),
		serverHandlers: v5Goroutines("distributed_client.(*Proxy).Read("),
		clientStreams:  v5Goroutines("grpc.newClientStreamWithParams"),
	}
}

// v5Settle polls until counts reach want or the deadline passes, then returns
// the final counts.
func v5Settle(blobDir string, cc *v5CountingCache, want v5Counts, d time.Duration) v5Counts {
	deadline := time.Now().Add(d)
	for {
		c := v5Measure(blobDir, cc)
		if c == want || time.Now().After(deadline) {
			return c
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestV5PartialReadLeak(t *testing.T) {
	type tc struct {
		window   int
		size     int64
		n        int
		readAll  bool
		readNone bool
	}
	var cases []tc
	for _, n := range []int{1, 10, 50} {
		cases = append(cases, tc{window: 1 << 20, size: 4 << 20, n: n})
	}
	// Production default stream window (8MiB) with a blob larger than it.
	cases = append(cases, tc{window: 8 << 20, size: 24 << 20, n: 1})
	cases = append(cases, tc{window: 8 << 20, size: 24 << 20, n: 3})
	// Controls: blob smaller than the window; full read before Close.
	cases = append(cases, tc{window: 8 << 20, size: 2 << 20, n: 10})
	cases = append(cases, tc{window: 1 << 20, size: 4 << 20, n: 10, readAll: true})
	for _, c := range cases {
		name := fmt.Sprintf("window=%d/size=%d/N=%d/readAll=%t", c.window, c.size, c.n, c.readAll)
		t.Run(name, func(t *testing.T) {
			flags.Set(t, "cache.distributed_cache.stream_window_size", c.window)
			te := getTestEnv(t, emptyUserMap)
			root := testfs.MakeTempDir(t)
			pc, err := pebble_cache.NewPebbleCache(te, &pebble_cache.Options{
				RootDirectory: root,
				MaxSizeBytes:  10_000_000_000,
			})
			require.NoError(t, err)
			require.NoError(t, pc.Start())
			t.Cleanup(func() { pc.Stop() })
			cc := &v5CountingCache{Cache: pc}
			blobDir := filepath.Join(root, "blobs")

			peer := fmt.Sprintf("localhost:%d", testport.FindFree(t))
			p := newProxy(t, te, cc, peer)
			require.NoError(t, p.StartListening())
			waitUntilServerIsAlive(peer)

			ctx, err := prefix.AttachUserPrefixToContext(context.Background(), te.GetAuthenticator())
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			rn, buf := testdigest.RandomCASResourceBuf(t, c.size)
			require.NoError(t, cc.Set(ctx, rn, buf))

			// Warm up: one full read establishes connections.
			r, err := p.RemoteReader(ctx, peer, rn, 0, 0)
			require.NoError(t, err)
			got, err := io.ReadAll(r)
			require.NoError(t, err)
			require.True(t, bytes.Equal(buf, got))
			require.NoError(t, r.Close())

			base := v5Settle(blobDir, cc, v5Counts{}, 5*time.Second)
			t.Logf("baseline after warmup: %+v", base)

			for i := 0; i < c.n; i++ {
				r, err := p.RemoteReader(ctx, peer, rn, 0, 0)
				require.NoError(t, err)
				if c.readAll {
					_, err = io.Copy(io.Discard, r)
					require.NoError(t, err)
				} else {
					one := make([]byte, 1)
					_, err = io.ReadFull(r, one)
					require.NoError(t, err)
				}
				require.NoError(t, r.Close())
			}
			after := v5Settle(blobDir, cc, base, 5*time.Second)
			t.Logf("V5ROW %s | before=%+v | after-close(ctx alive, 5s settle)=%+v", name, base, after)

			start := time.Now()
			cancel()
			final := v5Settle(blobDir, cc, base, 10*time.Second)
			t.Logf("V5ROW %s | after-cancel=%+v (%s)", name, final, time.Since(start))
			if after != base {
				t.Errorf("retained after Close with live ctx: fds=%d openReaders=%d serverHandlers=%d clientStreams=%d",
					after.fds-base.fds, after.openReaders-base.openReaders, after.serverHandlers-base.serverHandlers, after.clientStreams-base.clientStreams)
			}
		})
	}
}
