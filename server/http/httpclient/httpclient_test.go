package httpclient

import (
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

// Some callers create a client per operation and drop it afterwards (e.g.
// fetch_server, or ocifetcher when it evicts a cached puller). The client's
// idle keep-alive connections, each with two goroutines and a socket, must not
// outlive it. The test server never closes idle connections, so only the
// client can release them.
func TestDiscardedClientReleasesIdleConnections(t *testing.T) {
	for _, closeIdle := range []bool{false, true} {
		name := "idle_timeout"
		if closeIdle {
			name = "close_idle_connections"
		}
		t.Run(name, func(t *testing.T) {
			flags.Set(t, "http.client.allow_localhost", true)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("ok"))
			}))
			t.Cleanup(srv.Close)

			testleak.CheckGrowth(t, testleak.Growth{Iterations: 20}, func(i int) {
				client := New(nil, "test")
				if !closeIdle {
					tr := client.Transport.(*metricsTransport).inner.(*http.Transport)
					// net/http has no fake clock, so shorten the idle
					// timeout (before first use) to observe it expiring.
					// Zero means no timeout and is left as is.
					if tr.IdleConnTimeout != 0 {
						require.LessOrEqual(t, tr.IdleConnTimeout, 90*time.Second)
						tr.IdleConnTimeout = 10 * time.Millisecond
					}
				}
				resp, err := client.Get(srv.URL)
				require.NoError(t, err)
				_, err = io.Copy(io.Discard, resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				if closeIdle {
					client.CloseIdleConnections()
				}
			})
		})
	}
}
