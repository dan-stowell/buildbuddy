package fetch_server_test

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/remote_asset/fetch_server"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/scratchspace"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"

	rapb "github.com/buildbuddy-io/buildbuddy/proto/remote_asset"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

type v9Counts struct {
	goroutines   int
	httpClientG  int // persistConn read/write loops + http2 client conn loops
	sockets      int
	originOpen   int
	protos       string
}

func v9Measure(open func() int, protos func() string) v9Counts {
	buf := make([]byte, 1<<24)
	n := runtime.Stack(buf, true)
	c := v9Counts{goroutines: runtime.NumGoroutine(), originOpen: open(), protos: protos()}
	for g := range strings.SplitSeq(string(buf[:n]), "\n\n") {
		if strings.Contains(g, "net/http.(*persistConn).readLoop") || strings.Contains(g, "net/http.(*persistConn).writeLoop") || strings.Contains(g, "http2ClientConn") || strings.Contains(g, "(*clientConnReadLoop)") {
			c.httpClientG++
		}
	}
	ents, _ := os.ReadDir("/proc/self/fd")
	for _, e := range ents {
		tgt, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err == nil && strings.HasPrefix(tgt, "socket:") {
			c.sockets++
		}
	}
	return c
}

func runV9(t *testing.T, n int, withChecksum, useTLS bool) {
	flags.Set(t, "storage.tempdir", t.TempDir())
	require.NoError(t, scratchspace.Init())
	ctx := context.Background()
	te := testenv.GetTestEnv(t)
	clientConn := runFetchServer(ctx, t, te)
	fetchClient := rapb.NewFetchClient(clientConn)

	var mu sync.Mutex
	open := map[net.Conn]bool{}
	protoSeen := map[string]int{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		protoSeen[r.Proto]++
		mu.Unlock()
		fmt.Fprintf(w, "content-%s", r.URL.Path)
	})
	ts := httptest.NewUnstartedServer(h)
	ts.Config.ConnState = func(c net.Conn, s http.ConnState) {
		mu.Lock()
		defer mu.Unlock()
		switch s {
		case http.StateNew:
			open[c] = true
		case http.StateClosed, http.StateHijacked:
			delete(open, c)
		}
	}
	if useTLS {
		ts.EnableHTTP2 = true
		ts.StartTLS()
		p := filepath.Join(t.TempDir(), "ca.pem")
		require.NoError(t, os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0644))
		os.Setenv("SSL_CERT_FILE", p)
		// Use "localhost" so the cert (valid for 127.0.0.1 and example.com) matches IP SAN.
	} else {
		ts.Start()
	}
	defer ts.Close()
	openN := func() int { mu.Lock(); defer mu.Unlock(); return len(open) }
	protos := func() string { mu.Lock(); defer mu.Unlock(); return fmt.Sprint(protoSeen) }

	fetch := func(i int) {
		path := fmt.Sprintf("/f%d", i)
		content := "content-" + path
		var quals []*rapb.Qualifier
		if withChecksum {
			d, err := digest.Compute(bytes.NewReader([]byte(content)), repb.DigestFunction_SHA256)
			require.NoError(t, err)
			quals = append(quals, &rapb.Qualifier{Name: fetch_server.ChecksumQualifier, Value: checksumQualifierFromContent(t, d.GetHash(), repb.DigestFunction_SHA256)})
		}
		resp, err := fetchClient.FetchBlob(ctx, &rapb.FetchBlobRequest{Uris: []string{ts.URL + path}, Qualifiers: quals})
		require.NoError(t, err)
		require.Equal(t, int32(0), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	}
	fetch(-1) // warm-up
	time.Sleep(200 * time.Millisecond)
	base := v9Measure(openN, protos)
	for i := range n {
		fetch(i)
	}
	time.Sleep(500 * time.Millisecond)
	after := v9Measure(openN, protos)
	ts.CloseClientConnections()
	var closed v9Counts
	deadline := time.Now().Add(5 * time.Second)
	for {
		closed = v9Measure(openN, protos)
		if closed.httpClientG <= base.httpClientG-2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	closed = v9Measure(openN, protos)
	t.Logf("V9RESULT n=%d checksum=%v tls=%v | base: g=%d httpG=%d sock=%d originOpen=%d | after: g=%d httpG=%d sock=%d originOpen=%d | dG=%d dHttpG=%d dSock=%d | originClosed: g=%d httpG=%d sock=%d originOpen=%d | protos=%s",
		n, withChecksum, useTLS,
		base.goroutines, base.httpClientG, base.sockets, base.originOpen,
		after.goroutines, after.httpClientG, after.sockets, after.originOpen,
		after.goroutines-base.goroutines, after.httpClientG-base.httpClientG, after.sockets-base.sockets,
		closed.goroutines, closed.httpClientG, closed.sockets, closed.originOpen, after.protos)
}

func TestV9Explore(t *testing.T) {
	for _, n := range []int{10, 50} {
		for _, cs := range []bool{false, true} {
			t.Run(fmt.Sprintf("n%d_checksum%v", n, cs), func(t *testing.T) { runV9(t, n, cs, false) })
		}
	}
}

func TestV9ExploreTLS(t *testing.T) {
	for _, n := range []int{10, 50} {
		t.Run(fmt.Sprintf("n%d", n), func(t *testing.T) { runV9(t, n, true, true) })
	}
}
