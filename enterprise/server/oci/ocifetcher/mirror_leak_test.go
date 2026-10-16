package ocifetcher_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/oci/ocifetcher"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/stretchr/testify/require"
)

type countingMirrorTransport struct {
	inner       http.RoundTripper
	outstanding atomic.Int64
}
type countedMirrorBody struct {
	io.ReadCloser
	owner *countingMirrorTransport
}

func (b *countedMirrorBody) Close() error {
	b.owner.outstanding.Add(-1)
	return b.ReadCloser.Close()
}
func (c *countingMirrorTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.inner.RoundTrip(r)
	if err == nil {
		c.outstanding.Add(1)
		resp.Body = &countedMirrorBody{ReadCloser: resp.Body, owner: c}
	}
	return resp, err
}

// socketTargets is a temporary local FD check until testleak supports FDs.
func socketTargets(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	var targets []string
	for _, e := range entries {
		target, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err == nil && strings.HasPrefix(target, "socket:") {
			targets = append(targets, target)
		}
	}
	return targets
}

func TestMirrorFallbackClosesResponseBodies(t *testing.T) {
	for _, n := range []int{1, 10, 50} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			testleak.Check(t)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "layer") }))
			t.Cleanup(origin.Close)
			mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				io.WriteString(w, "mirror unavailable")
			}))
			t.Cleanup(mirror.Close)
			// Disable keep-alive so retained sockets cannot be mistaken for an idle pool.
			tr := &http.Transport{DisableKeepAlives: true}
			t.Cleanup(tr.CloseIdleConnections)
			counter := &countingMirrorTransport{inner: tr}
			client := &http.Client{Transport: ocifetcher.NewMirrorTransport(counter, []interfaces.MirrorConfig{{OriginalURL: origin.URL, MirrorURL: mirror.URL}})}
			before := socketTargets(t)
			goroutinesBefore := runtime.NumGoroutine()
			for range n {
				resp, err := client.Get(origin.URL + "/v2/image/blobs/sha256:abc")
				require.NoError(t, err)
				data, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, "layer", string(data))
			}
			settled := func() bool { return counter.outstanding.Load() == 0 && len(socketTargets(t)) <= len(before) }
			ok := assertSettled(settled, time.Second)
			after := socketTargets(t)
			t.Logf("N=%d unclosed_bodies=%d sockets_before=%d sockets_after=%d goroutines_before=%d goroutines_after=%d socket_targets=%v", n, counter.outstanding.Load(), len(before), len(after), goroutinesBefore, runtime.NumGoroutine(), after)
			if !ok {
				stacks := make([]byte, 256*1024)
				t.Logf("goroutines after fetch completion:\n%s", stacks[:runtime.Stack(stacks, true)])
			}
			require.True(t, ok, "completed mirror fallback must close every response body and release sockets")
		})
	}
}

func assertSettled(check func() bool, deadline time.Duration) bool {
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return true
		}
		select {
		case <-timer.C:
			return check()
		case <-ticker.C:
		}
	}
}

// Production RPC cancellation is an important control: it can reclaim sockets
// even though the mirror response body was never explicitly closed.
func TestMirrorFallbackCancellationReleasesSockets(t *testing.T) {
	for _, n := range []int{1, 10, 50} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			testleak.Check(t)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "layer") }))
			t.Cleanup(origin.Close)
			mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				io.WriteString(w, "mirror unavailable")
			}))
			t.Cleanup(mirror.Close)
			tr := &http.Transport{DisableKeepAlives: true}
			t.Cleanup(tr.CloseIdleConnections)
			counter := &countingMirrorTransport{inner: tr}
			client := &http.Client{Transport: ocifetcher.NewMirrorTransport(counter, []interfaces.MirrorConfig{{OriginalURL: origin.URL, MirrorURL: mirror.URL}})}
			before := socketTargets(t)
			goroutinesBefore := runtime.NumGoroutine()
			for range n {
				ctx, cancel := context.WithCancel(context.Background())
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL+"/v2/image/blobs/sha256:abc", nil)
				require.NoError(t, err)
				resp, err := client.Do(req)
				if err != nil {
					cancel()
					require.NoError(t, err)
				}
				_, err = io.Copy(io.Discard, resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				cancel()
			}
			require.True(t, assertSettled(func() bool { return len(socketTargets(t)) <= len(before) && runtime.NumGoroutine() <= goroutinesBefore }, time.Second))
			t.Logf("N=%d after_cancellation unclosed_bodies=%d sockets_before=%d sockets_after=%d goroutines_before=%d goroutines_after=%d", n, counter.outstanding.Load(), len(before), len(socketTargets(t)), goroutinesBefore, runtime.NumGoroutine())
		})
	}
}
