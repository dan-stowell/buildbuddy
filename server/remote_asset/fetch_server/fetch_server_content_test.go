package fetch_server_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/remote_asset/fetch_server"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/cachetools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/scratchspace"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"

	rapb "github.com/buildbuddy-io/buildbuddy/proto/remote_asset"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	gcodes "google.golang.org/grpc/codes"
)

func readFetchedBlob(ctx context.Context, t *testing.T, bs bspb.ByteStreamClient, instanceName string, resp *rapb.FetchBlobResponse) []byte {
	var got bytes.Buffer
	rn := digest.NewCASResourceName(resp.GetBlobDigest(), instanceName, resp.GetDigestFunction())
	require.NoError(t, cachetools.GetBlob(ctx, bs, rn, &got))
	return got.Bytes()
}

func gzipBytes(t *testing.T, b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(b)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// An origin that labels an already-compressed archive with
// "Content-Encoding: gzip" (a common object-store configuration) must have
// the archive bytes mirrored as published, so that the archive's published
// checksum validates.
func TestFetchBlob_ContentEncodingGzipArchive(t *testing.T) {
	flags.Set(t, "storage.tempdir", t.TempDir())
	require.NoError(t, scratchspace.Init())
	ctx := context.Background()
	te := testenv.GetTestEnv(t)
	conn := runFetchServer(ctx, t, te)
	client := rapb.NewFetchClient(conn)
	bs := bspb.NewByteStreamClient(conn)

	archive := gzipBytes(t, []byte("pretend this is a tar file\n"))
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-gzip")
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(archive)
	}))
	defer origin.Close()
	uri := origin.URL + "/release-1.0.tar.gz"

	archiveDigest, err := digest.Compute(bytes.NewReader(archive), repb.DigestFunction_SHA256)
	require.NoError(t, err)

	t.Run("with_checksum", func(t *testing.T) {
		resp, err := client.FetchBlob(ctx, &rapb.FetchBlobRequest{
			Uris: []string{uri},
			Qualifiers: []*rapb.Qualifier{{
				Name:  fetch_server.ChecksumQualifier,
				Value: checksumQualifierFromContent(t, archiveDigest.GetHash(), repb.DigestFunction_SHA256),
			}},
			DigestFunction: repb.DigestFunction_SHA256,
		})
		require.NoError(t, err)
		require.Equal(t, int32(gcodes.OK), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
		require.Equal(t, archive, readFetchedBlob(ctx, t, bs, "", resp))
	})
	t.Run("without_checksum", func(t *testing.T) {
		resp, err := client.FetchBlob(ctx, &rapb.FetchBlobRequest{
			Uris:           []string{uri},
			DigestFunction: repb.DigestFunction_SHA256,
		})
		require.NoError(t, err)
		require.Equal(t, int32(gcodes.OK), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
		require.Equal(t, archive, readFetchedBlob(ctx, t, bs, "", resp), "mirrored bytes differ from the published archive")
	})
}

// Responses that are not a successful transfer of the full resource must not
// be returned as the fetched blob.
func TestFetchBlob_NonContentStatusIsNotABlob(t *testing.T) {
	flags.Set(t, "storage.tempdir", t.TempDir())
	require.NoError(t, scratchspace.Init())
	ctx := context.Background()
	te := testenv.GetTestEnv(t)
	conn := runFetchServer(ctx, t, te)
	client := rapb.NewFetchClient(conn)
	bs := bspb.NewByteStreamClient(conn)

	for _, code := range []int{
		http.StatusNoContent,
		http.StatusPartialContent,
		http.StatusMultipleChoices,
		http.StatusFound, // without a Location header
		http.StatusNotModified,
	} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			body := fmt.Sprintf("<html>status %d page</html>", code)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				w.Write([]byte(body))
			}))
			defer origin.Close()
			resp, err := client.FetchBlob(ctx, &rapb.FetchBlobRequest{
				Uris:           []string{origin.URL + "/file.zip"},
				DigestFunction: repb.DigestFunction_SHA256,
			})
			require.NoError(t, err)
			if resp.GetStatus().GetCode() == int32(gcodes.OK) {
				got := readFetchedBlob(ctx, t, bs, "", resp)
				t.Fatalf("HTTP %d response returned as fetched blob (%d bytes: %q)", code, len(got), got)
			}
		})
	}
}

// Randomized check that a checksum-qualified FetchBlob only ever returns
// content matching one of the requested checksums, across cache states,
// digest functions, multiple URIs and checksum alternatives.
func TestFetchBlob_RandomizedChecksumIntegrity(t *testing.T) {
	flags.Set(t, "storage.tempdir", t.TempDir())
	require.NoError(t, scratchspace.Init())
	seed := time.Now().UnixNano()
	if s := os.Getenv("FETCH_TEST_SEED"); s != "" {
		var err error
		seed, err = strconv.ParseInt(s, 10, 64)
		require.NoError(t, err)
	}
	t.Logf("seed=%d", seed)
	rng := rand.New(rand.NewSource(seed))
	// Client reads request ZSTD for larger blobs, which requires transcoding.
	flags.Set(t, "cache.zstd_transcoding_enabled", true)

	ctx := context.Background()
	te := testenv.GetTestEnv(t)
	conn := runFetchServer(ctx, t, te)
	client := rapb.NewFetchClient(conn)
	bs := bspb.NewByteStreamClient(conn)

	funcs := []repb.DigestFunction_Value{
		repb.DigestFunction_SHA256, repb.DigestFunction_SHA384, repb.DigestFunction_SHA512,
		repb.DigestFunction_SHA1, repb.DigestFunction_BLAKE3,
	}
	hashOf := func(b []byte, f repb.DigestFunction_Value) string {
		d, err := digest.Compute(bytes.NewReader(b), f)
		require.NoError(t, err)
		return d.GetHash()
	}

	var contents [][]byte
	for range 6 {
		b := make([]byte, rng.Intn(300))
		rng.Read(b)
		contents = append(contents, b)
	}
	// Content index 5 is never served nor cached (checksum for unknown content).
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i, err := strconv.Atoi(r.URL.Query().Get("c"))
		if err != nil || i >= 5 {
			http.NotFound(w, r)
			return
		}
		body := contents[i]
		switch r.URL.Query().Get("m") {
		case "chunked":
			w.(http.Flusher).Flush()
			w.Write(body)
		case "short":
			// Advertise full length but close early.
			w.Header().Set("Content-Length", strconv.Itoa(len(body)+10))
			w.Write(body)
		case "error":
			w.WriteHeader(500)
			w.Write(body)
		default:
			w.Write(body)
		}
	}))
	defer origin.Close()

	const trials = 300
	for trial := range trials {
		instance := fmt.Sprintf("trial-%d", trial)
		storage := funcs[rng.Intn(len(funcs))]
		reqFunc := storage
		if storage == repb.DigestFunction_SHA256 && rng.Intn(2) == 0 {
			reqFunc = repb.DigestFunction_UNKNOWN
		}
		// Pre-populate the cache.
		type cached struct {
			c int
			f repb.DigestFunction_Value
		}
		var cachedEntries []cached
		for n := rng.Intn(3); n > 0; n-- {
			c := rng.Intn(5)
			f := funcs[rng.Intn(len(funcs))]
			_, err := cachetools.UploadBlob(ctx, bs, instance, f, bytes.NewReader(contents[c]))
			require.NoError(t, err)
			cachedEntries = append(cachedEntries, cached{c, f})
		}
		// Checksum alternatives.
		type ck struct {
			c int
			f repb.DigestFunction_Value
		}
		var cks []ck
		sri := ""
		for n := 1 + rng.Intn(3); n > 0; n-- {
			c := rng.Intn(6)
			f := funcs[rng.Intn(len(funcs))]
			cks = append(cks, ck{c, f})
			if sri != "" {
				sri += " "
			}
			sri += checksumQualifierFromContent(t, hashOf(contents[c], f), f)
		}
		// URIs.
		modes := []string{"plain", "chunked", "short", "error"}
		var uris []string
		type served struct {
			c    int
			good bool
		}
		var servedURIs []served
		for n := 1 + rng.Intn(3); n > 0; n-- {
			c := rng.Intn(6)
			m := modes[rng.Intn(len(modes))]
			uris = append(uris, fmt.Sprintf("%s/f?c=%d&m=%s", origin.URL, c, m))
			servedURIs = append(servedURIs, served{c, c < 5 && (m == "plain" || m == "chunked")})
		}
		desc := fmt.Sprintf("trial=%d storage=%s cache=%v sri=%q uris=%v", trial, reqFunc, cachedEntries, sri, uris)

		resp, err := client.FetchBlob(ctx, &rapb.FetchBlobRequest{
			InstanceName:   instance,
			Uris:           uris,
			Qualifiers:     []*rapb.Qualifier{{Name: fetch_server.ChecksumQualifier, Value: sri}},
			DigestFunction: reqFunc,
		})
		require.NoError(t, err, desc)

		satisfiable := false
		for _, k := range cks {
			for _, e := range cachedEntries {
				if bytes.Equal(contents[e.c], contents[k.c]) && e.f == k.f {
					satisfiable = true
				}
			}
			for _, s := range servedURIs {
				if s.good && bytes.Equal(contents[s.c], contents[k.c]) {
					satisfiable = true
				}
			}
		}
		if resp.GetStatus().GetCode() != int32(gcodes.OK) {
			require.False(t, satisfiable, "satisfiable request failed: %s: %s", desc, resp.GetStatus().GetMessage())
			continue
		}
		require.Equal(t, storage, resp.GetDigestFunction(), desc)
		got := readFetchedBlob(ctx, t, bs, instance, resp)
		require.Equal(t, int64(len(got)), resp.GetBlobDigest().GetSizeBytes(), desc)
		matched := false
		for _, k := range cks {
			if hashOf(got, k.f) == hashOf(contents[k.c], k.f) {
				matched = true
			}
		}
		require.True(t, matched, "returned blob matches no requested checksum: %s", desc)
	}
}

// An origin that closes the connection before sending the advertised
// Content-Length must produce a prompt fetch failure (and allow falling back
// to a later URI), rather than an upload that never completes.
func TestFetchBlob_TruncatedKnownLengthBody(t *testing.T) {
	for _, zstd := range []bool{false, true} {
		for _, withFallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("zstd=%t/fallback=%t", zstd, withFallback), func(t *testing.T) {
				flags.Set(t, "cache.zstd_transcoding_enabled", zstd)
				flags.Set(t, "cache.client.enable_upload_compression", zstd)
				flags.Set(t, "storage.tempdir", t.TempDir())
				require.NoError(t, scratchspace.Init())
				ctx := context.Background()
				te := testenv.GetTestEnv(t)
				conn := runFetchServer(ctx, t, te)
				client := rapb.NewFetchClient(conn)

				body := bytes.Repeat([]byte("0123456789abcdef"), 64)
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/good" {
						w.Write(body)
						return
					}
					// Advertise the full length but send only half the body.
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
					w.Write(body[:len(body)/2])
				}))
				defer origin.Close()
				uris := []string{origin.URL + "/truncated"}
				if withFallback {
					uris = append(uris, origin.URL+"/good")
				}
				d, err := digest.Compute(bytes.NewReader(body), repb.DigestFunction_SHA256)
				require.NoError(t, err)

				rpcCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				start := time.Now()
				resp, err := client.FetchBlob(rpcCtx, &rapb.FetchBlobRequest{
					Uris: uris,
					Qualifiers: []*rapb.Qualifier{{
						Name:  fetch_server.ChecksumQualifier,
						Value: checksumQualifierFromContent(t, d.GetHash(), repb.DigestFunction_SHA256),
					}},
					DigestFunction: repb.DigestFunction_SHA256,
				})
				require.NoError(t, err, "FetchBlob did not return within %s", time.Since(start))
				if withFallback {
					require.Equal(t, int32(gcodes.OK), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
					require.Equal(t, origin.URL+"/good", resp.GetUri())
				} else {
					require.NotEqual(t, int32(gcodes.OK), resp.GetStatus().GetCode())
				}
			})
		}
	}
}
