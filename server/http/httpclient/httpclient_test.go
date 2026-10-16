package httpclient

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"

	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
)

func TestBlockingDialerControl(t *testing.T) {
	tests := []struct {
		name           string
		address        string
		allowLocalhost bool
		wantError      bool
	}{
		// Public addresses should be allowed by default.
		{
			name:      "public address",
			address:   "8.8.8.8:80",
			wantError: false,
		},
		// IPv4 and IPv6 loopback addresses should only be allowed when the
		// localhost flag is enabled.
		{
			name:      "localhost",
			address:   "127.0.0.1:80",
			wantError: true,
		},
		{
			name:           "localhost allowed by flag",
			address:        "127.0.0.1:80",
			allowLocalhost: true,
			wantError:      false,
		},
		{
			name:      "IPv6 localhost",
			address:   "[::1]:80",
			wantError: true,
		},
		{
			name:           "IPv6 localhost allowed by flag",
			address:        "[::1]:80",
			allowLocalhost: true,
			wantError:      false,
		},
		// Private and link-local addresses should always be blocked.
		{
			name:      "IPv4 private address",
			address:   "10.0.0.1:80",
			wantError: true,
		},
		{
			name:      "IPv6 private address",
			address:   "[fd00::1]:80",
			wantError: true,
		},
		{
			name:      "IPv4 link-local address",
			address:   "169.254.169.254:80",
			wantError: true,
		},
		{
			name:      "IPv6 link-local address",
			address:   "[fe80::1]:80",
			wantError: true,
		},
		{
			name:           "IPv4 unspecified address even with localhost allowed",
			address:        "0.0.0.0:80",
			allowLocalhost: true,
			wantError:      true,
		},
		{
			name:      "IPv4 unspecified address",
			address:   "0.0.0.0:80",
			wantError: true,
		},
		{
			name:      "IPv6 unspecified address",
			address:   "[::]:80",
			wantError: true,
		},
		{
			name:      "IPv4 broadcast address",
			address:   "255.255.255.255:80",
			wantError: true,
		},
		{
			name:      "IPv6 multicast address",
			address:   "[ff02::1]:80",
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			flags.Set(t, "http.client.allow_localhost", test.allowLocalhost)
			err := blockingDialerControl(nil)("tcp", test.address, nil)
			if test.wantError {
				require.ErrorContains(t, err, "IP address not allowed")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// Each operation discards its client after fully consuming the response.
// Keep the server alive so upstream shutdown cannot hide retained connections.
func TestDiscardedClientIdleConnections(t *testing.T) {
	for _, closeExplicitly := range []bool{false, true} {
		for _, n := range []int{1, 10, 50} {
			t.Run(fmt.Sprintf("close=%t/N=%d", closeExplicitly, n), func(t *testing.T) {
				flags.Set(t, "http.client.allow_localhost", true)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
				defer srv.Close()
				testleak.CheckGrowth(t, testleak.Growth{Iterations: n}, func(i int) {
					client := New(nil, "idle_cleanup_test")
					inner := client.Transport.(*metricsTransport).inner.(*http.Transport)
					t.Cleanup(inner.CloseIdleConnections)
					if !closeExplicitly && inner.IdleConnTimeout > 0 {
						// Scale the configured lifetime before first use. A zero (unbounded)
						// production timeout stays zero so baseline reproduces the leak.
						require.Equal(t, 90*time.Second, inner.IdleConnTimeout)
						inner.IdleConnTimeout = 20 * time.Millisecond
					}
					resp, err := client.Get(srv.URL)
					require.NoError(t, err)
					_, err = io.Copy(io.Discard, resp.Body)
					require.NoError(t, err)
					require.NoError(t, resp.Body.Close())
					if closeExplicitly {
						client.CloseIdleConnections()
					}
				})
			})
		}
	}
}
