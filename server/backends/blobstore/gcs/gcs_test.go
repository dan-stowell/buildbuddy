package gcs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
)

func TestReaderWriterCloseGrowth(t *testing.T) {
	testleak.Check(t)
	testleak.CheckFDs(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Length", "1048576")
			_, _ = io.WriteString(w, strings.Repeat("x", 1<<20))
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(server.CloseClientConnections)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client, err := storage.NewClient(context.Background(), option.WithEndpoint(server.URL),
		option.WithoutAuthentication(), option.WithHTTPClient(&http.Client{Transport: transport}))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store := &GCSBlobStore{bucketHandle: client.Bucket("bucket"), metricLabel: "gcs/test"}
	// Parent stays alive through the growth check; operation Close owns release.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	testleak.CheckGrowth(t, testleak.Growth{Iterations: 20}, func(int) {
		r, err := store.Reader(ctx, "object", 0, 0)
		require.NoError(t, err)
		_, err = r.Read(make([]byte, 1))
		require.NoError(t, err)
		require.NoError(t, r.Close())
		w, err := store.Writer(ctx, "object")
		require.NoError(t, err)
		_, err = w.Write([]byte("data"))
		require.NoError(t, err)
		require.NoError(t, w.Close()) // Abort without committing.
		transport.CloseIdleConnections()
	})
}
