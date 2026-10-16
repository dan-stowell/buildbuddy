package gcs

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"hash/crc32"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
)

// A corrupt compressed object must not leave ownership of its HTTP body with
// a caller that only received an error and no reader to close.
func TestReaderInvalidCompressionReleasesBody(t *testing.T) {
	for _, n := range []int{1, 10, 50} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			testleak.Check(t)
			testleak.CheckFDs(t)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			body := "\x1f\x8b\x00" + strings.Repeat("x", 1<<20)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(server.Close)
			// Force cleanup of leaked active connections after the growth measurement,
			// so one failing subtest does not contaminate the next.
			t.Cleanup(server.CloseClientConnections)
			transport := &http.Transport{}
			t.Cleanup(transport.CloseIdleConnections)
			client, err := storage.NewClient(context.Background(),
				option.WithEndpoint(server.URL), option.WithoutAuthentication(),
				option.WithHTTPClient(&http.Client{Transport: transport}))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			store := &GCSBlobStore{gcsClient: client, bucketHandle: client.Bucket("bucket"), compress: true, metricLabel: "gcs/test"}
			// This context intentionally outlives each operation, as a server context
			// does. Reader must release the response body before returning its error.
			testleak.CheckGrowth(t, testleak.Growth{Iterations: n}, func(i int) {
				reader, err := store.Reader(ctx, "corrupt", 0, 0)
				require.Error(t, err)
				require.Nil(t, reader)
				transport.CloseIdleConnections()
			})
		})
	}
}

// Exercise the ordinary upload release events using the real HTTP upload
// implementation. No credentials or external GCS service are needed.
func TestUploadLifecycle(t *testing.T) {
	for _, kind := range []string{"plain", "compressed", "conditional-buffered", "conditional-unbuffered"} {
		for _, end := range []string{"commit", "close", "cancel", "precondition"} {
			t.Run(kind+"/"+end, func(t *testing.T) {
				testleak.Check(t)
				testleak.CheckFDs(t)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					checksum := crc32.New(crc32.MakeTable(crc32.Castagnoli))
					mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
					if err == nil && strings.HasPrefix(mediaType, "multipart/") {
						parts := multipart.NewReader(r.Body, params["boundary"])
						for {
							part, err := parts.NextPart()
							if err == io.EOF {
								break
							}
							if err != nil {
								http.Error(w, err.Error(), 400)
								return
							}
							checksum.Reset()
							_, _ = io.Copy(checksum, part)
						}
					} else {
						_, _ = io.Copy(checksum, r.Body)
					}
					crc := base64.StdEncoding.EncodeToString(binary.BigEndian.AppendUint32(nil, checksum.Sum32()))
					if end == "precondition" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusPreconditionFailed)
						_, _ = io.WriteString(w, `{"error":{"code":412,"message":"precondition failed"}}`)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"bucket":"bucket","name":"object","generation":"1","size":"4","crc32c":%q}`, crc)
				}))
				t.Cleanup(server.Close)
				t.Cleanup(server.CloseClientConnections)
				transport := &http.Transport{}
				t.Cleanup(transport.CloseIdleConnections)
				client, err := storage.NewClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication(), option.WithHTTPClient(&http.Client{Transport: transport}))
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, client.Close()) })
				store := &GCSBlobStore{bucketHandle: client.Bucket("bucket"), compress: kind == "compressed", metricLabel: "gcs/test"}
				testleak.CheckGrowth(t, testleak.Growth{Iterations: 50}, func(i int) {
					ctx, cancel := context.WithCancel(context.Background())
					// Keep the parent alive through the growth measurement so that
					// its cancellation cannot hide a missing operation Close.
					t.Cleanup(cancel)
					var w interfaces.CommittedWriteCloser
					if strings.HasPrefix(kind, "conditional") {
						estimated := int64(0)
						if kind == "conditional-buffered" {
							estimated = 16 << 20
						}
						w, err = store.ConditionalWriter(ctx, "object", false, time.Now(), estimated)
					} else {
						w, err = store.Writer(ctx, "object")
					}
					require.NoError(t, err)
					_, writeErr := w.Write([]byte("data"))
					if end == "cancel" {
						cancel()
					}
					if end == "commit" || end == "precondition" {
						commitErr := w.Commit()
						if end == "commit" {
							require.NoError(t, writeErr)
							require.NoError(t, commitErr)
						} else {
							require.Error(t, commitErr)
						}
					}
					require.NoError(t, w.Close())
					transport.CloseIdleConnections()
				})
			})
		}
	}
}

func TestReaderLifecycle(t *testing.T) {
	for _, end := range []string{"partial-close", "cancel", "404"} {
		t.Run(end, func(t *testing.T) {
			testleak.Check(t)
			testleak.CheckFDs(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if end == "404" {
					http.Error(w, "missing", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Length", "1048576")
				_, _ = io.WriteString(w, strings.Repeat("x", 1<<20))
			}))
			t.Cleanup(server.Close)
			t.Cleanup(server.CloseClientConnections)
			transport := &http.Transport{}
			t.Cleanup(transport.CloseIdleConnections)
			client, err := storage.NewClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication(), option.WithHTTPClient(&http.Client{Transport: transport}))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			store := &GCSBlobStore{bucketHandle: client.Bucket("bucket"), metricLabel: "gcs/test"}
			testleak.CheckGrowth(t, testleak.Growth{Iterations: 50}, func(i int) {
				ctx, cancel := context.WithCancel(context.Background())
				// Keep the parent alive through the growth measurement so that
				// its cancellation cannot hide a missing operation Close.
				t.Cleanup(cancel)
				r, err := store.Reader(ctx, "object", 0, 0)
				if end == "404" {
					require.Error(t, err)
					require.Nil(t, r)
					transport.CloseIdleConnections()
					return
				}
				require.NoError(t, err)
				_, err = r.Read(make([]byte, 1))
				require.NoError(t, err)
				if end == "cancel" {
					cancel()
				}
				require.NoError(t, r.Close())
				transport.CloseIdleConnections()
			})
		})
	}
}
