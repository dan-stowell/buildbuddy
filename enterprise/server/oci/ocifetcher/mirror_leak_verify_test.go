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
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/oci/ocifetcher"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
)

func verifySocketCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		target, _ := os.Readlink("/proc/self/fd/" + e.Name())
		if strings.HasPrefix(target, "socket:[") {
			n++
		}
	}
	return n
}

func TestIndependentMirrorFallbackResources(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		for _, n := range []int{1, 10, 50} {
			t.Run(fmt.Sprintf("disableKeepAlive=%t/N=%d", disabled, n), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
				mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); io.WriteString(w, "unavailable") }))
				tr := &http.Transport{DisableKeepAlives: disabled}
				t.Cleanup(func() { cancel(); tr.CloseIdleConnections(); mirror.Close(); origin.Close() })
				testleak.Check(t)
				// Capture a stable baseline after listeners exist; close idle pools before measuring.
				beforeFD, beforeG := verifySocketCount(t), runtime.NumGoroutine()
				wrapped := ocifetcher.NewMirrorTransport(tr, []interfaces.MirrorConfig{{OriginalURL: origin.URL, MirrorURL: mirror.URL}})
				for range n {
					req, _ := http.NewRequestWithContext(ctx, "GET", origin.URL+"/v2/", nil)
					rsp, err := wrapped.RoundTrip(req)
					if err != nil {
						t.Fatal(err)
					}
					_, err = io.Copy(io.Discard, rsp.Body)
					rsp.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				tr.CloseIdleConnections()
				deadline := time.Now().Add(time.Second)
				for verifySocketCount(t) > beforeFD && time.Now().Before(deadline) {
					runtime.Gosched()
					time.Sleep(10 * time.Millisecond)
				}
				afterFD, afterG := verifySocketCount(t), runtime.NumGoroutine()
				t.Logf("N=%d disableKeepAlive=%t sockets=%d->%d goroutines=%d->%d", n, disabled, beforeFD, afterFD, beforeG, afterG)
				// Cancel only after recording owner-finished resources; avoid contaminating later subtests.
				if afterFD > beforeFD {
					t.Errorf("fallback retained %d socket FDs after returned body closed and idle pool closed", afterFD-beforeFD)
				}
			})
		}
	}
}
