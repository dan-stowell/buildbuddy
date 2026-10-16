package gcs

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
)

// newHTTPStore returns a compressing GCSBlobStore whose HTTP client talks to
// a local server that serves body for every object read.
func newHTTPStore(t *testing.T, body []byte) *GCSBlobStore {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	client, err := storage.NewClient(context.Background(),
		option.WithEndpoint(srv.URL+"/storage/v1/"),
		option.WithoutAuthentication(),
		option.WithHTTPClient(srv.Client()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return &GCSBlobStore{
		gcsClient:    client,
		bucketHandle: client.Bucket("bucket"),
		compress:     true,
		metricLabel:  "gcs/test",
	}
}

// A blob that starts with the gzip magic bytes but has an invalid header makes
// decompressor initialization fail. Reader must close the GCS object reader
// (and so the HTTP response body) since the caller gets nothing to close.
func TestReaderClosesObjectWhenDecompressorInitFails(t *testing.T) {
	testleak.Check(t)
	testleak.CheckFDs(t)
	// Gzip magic, then compression method 0 (only 8 is valid), then enough
	// data that the response can't be fully buffered by the client.
	body := append([]byte{0x1f, 0x8b, 0x00, 0x00, 0, 0, 0, 0, 0, 0}, bytes.Repeat([]byte{'x'}, 1<<20)...)
	g := newHTTPStore(t, body)
	// The parent context outlives every read, like a server context would.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	testleak.CheckGrowth(t, testleak.Growth{Iterations: 10}, func(int) {
		rc, err := g.Reader(ctx, "corrupt", 0, 0)
		require.Error(t, err)
		require.Nil(t, rc)
	})
}
