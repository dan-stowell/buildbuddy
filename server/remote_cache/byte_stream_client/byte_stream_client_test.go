package byte_stream_client

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"github.com/buildbuddy-io/buildbuddy/server/real_environment"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/jonboulle/clockwork"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"io"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/buildbuddy-io/buildbuddy/server/util/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	zipb "github.com/buildbuddy-io/buildbuddy/proto/zip"
)

var (
	someFilesRunfilePath    string
	noFilesRunfilePath      string
	tooManyFilesRunfilePath string
)

func TestManifest_SomeFilesZip(t *testing.T) {
	expected := &zipb.Manifest{
		Entry: []*zipb.ManifestEntry{
			{
				Name:             "f0.txt",
				UncompressedSize: 5,
				CompressedSize:   7,
				Compression:      zipb.ManifestEntry_COMPRESSION_TYPE_FLATE,
				Crc32:            2745310273,
			},
			{
				Name:             "f1.txt",
				UncompressedSize: 10,
				CompressedSize:   12,
				HeaderOffset:     91,
				Compression:      zipb.ManifestEntry_COMPRESSION_TYPE_FLATE,
				Crc32:            1157536424,
			},
			{
				Name:             "f2.txt",
				UncompressedSize: 36,
				CompressedSize:   38,
				HeaderOffset:     187,
				Compression:      zipb.ManifestEntry_COMPRESSION_TYPE_FLATE,
				Crc32:            869932603,
			},
		},
	}
	path, err := runfiles.Rlocation(someFilesRunfilePath)
	require.NoError(t, err)
	bytes, err := os.ReadFile(path)
	assert.NoError(t, err)
	manifest, err := ParseZipManifestFooter(bytes, 0, int64(len(bytes)))
	assert.NoError(t, err)
	if !proto.Equal(expected, manifest) {
		t.Fatalf("Incorrect manifest. Expected: %v\n Actual: %v", expected, manifest)
	}
}

func TestManifest_NoFilesZip(t *testing.T) {
	expected := &zipb.Manifest{}
	path, err := runfiles.Rlocation(noFilesRunfilePath)
	require.NoError(t, err)
	bytes, err := os.ReadFile(path)
	assert.NoError(t, err)
	manifest, err := ParseZipManifestFooter(bytes, 0, int64(len(bytes)))
	assert.NoError(t, err)
	if !proto.Equal(expected, manifest) {
		t.Fatalf("Incorrect manifest. Expected: %v\n Actual: %v", expected, manifest)
	}
}

func TestManifest_TooManyFilesZip(t *testing.T) {
	path, err := runfiles.Rlocation(tooManyFilesRunfilePath)
	require.NoError(t, err)
	bytes, err := os.ReadFile(path)
	assert.NoError(t, err)

	offset := len(bytes) - 65536
	_, err = ParseZipManifestFooter(bytes[65536:], int64(offset), int64(len(bytes)))
	if !strings.Contains(err.Error(), "code = Unimplemented") {
		t.Fatalf("Unexpectedly parsed very large manifest.")
	}
}

func createBytestreamer(b []byte) Bytestreamer {
	return func(ctx context.Context, url *url.URL, offset int64, limit int64, writer io.Writer) error {
		writer.Write(b[offset : offset+limit])
		return nil
	}
}

func validateZipContents(t *testing.T, ctx context.Context, entry *zipb.ManifestEntry, expectedContent string, streamer Bytestreamer) {
	var buf bytes.Buffer
	streamSingleFileFromBytestreamZipInternal(ctx, nil, entry, &buf, streamer)
	out := make([]byte, entry.GetUncompressedSize())
	_, err := io.ReadFull(&buf, out)
	if err != nil {
		t.Fatal(err.Error())
	}
	if expectedContent != string(out) {
		t.Fatalf("Got unexpected content, expected: %s, actual: %s", expectedContent, string(out))
	}
}

func TestReadZipFileContents(t *testing.T) {
	path, err := runfiles.Rlocation(someFilesRunfilePath)
	require.NoError(t, err)
	b, err := os.ReadFile(path)
	assert.NoError(t, err)
	manifest, err := ParseZipManifestFooter(b, 0, int64(len(b)))
	assert.NoError(t, err)

	streamer := createBytestreamer(b)
	ctx := context.Background()
	validateZipContents(t, ctx, manifest.GetEntry()[0], "bazel", streamer)
	validateZipContents(t, ctx, manifest.GetEntry()[1], "buildbuddy", streamer)
	validateZipContents(t, ctx, manifest.GetEntry()[2], "san dimas high school football rules", streamer)
}

// TestV4DistinctPermittedTargets measures resources after every request has
// returned. Synthetic userinfo is deliberately not a credential.
func TestV4DistinctPermittedTargets(t *testing.T) {
	for _, n := range []int{1, 10, 50} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			testleak.Check(t)
			env := real_environment.NewRealEnv(nil)
			flags.Set(t, "app.restrict_bytestream_dialing", true)
			flags.Set(t, "app.cache_api_url", url.URL{Scheme: "grpcs", Host: "cache.example.com"})
			clock := clockwork.NewFakeClock()
			env.SetClock(clock)
			p := NewPooledByteStreamClient(env)
			t.Cleanup(func() { p.close(context.Background()) })
			before := runtime.NumGoroutine()
			for i := range n {
				u, err := url.Parse(fmt.Sprintf("bytestream://synthetic-%d@localhost:1/blobs/abc/1", i))
				require.NoError(t, err)
				require.True(t, isPermittedForDial(getTargetForURL(u, true)))
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				require.Error(t, p.StreamBytestreamFile(ctx, u, io.Discard))
			}
			t.Logf("N=%d cached=%d goroutine_delta=%d", n, len(p.connPoolMap), runtime.NumGoroutine()-before)

			for _, cp := range p.connPoolMap {
				require.Zero(t, cp.inFlight)
			}
			clock.Advance(*poolCacheIdleTimeout + time.Second)
			conn, release, err := p.getGrpcClientConnPoolForURL("grpcs://localhost:1")
			require.NoError(t, err)
			require.NotNil(t, conn)
			require.Len(t, p.connPoolMap, 1, "expired pools should close on the next fetch")
			// Acquiring the same active pool while sweeping must not close it.
			clock.Advance(*poolCacheIdleTimeout + time.Second)
			_, releaseOther, err := p.getGrpcClientConnPoolForURL("grpcs://localhost:2")
			require.NoError(t, err)
			require.Len(t, p.connPoolMap, 2, "in-flight pool must survive sweep")
			releaseOther()
			release()
			require.NoError(t, p.close(context.Background()))
			require.Empty(t, p.connPoolMap)
			_, _, err = p.getGrpcClientConnPoolForURL("grpcs://localhost:1")
			require.Error(t, err)

		})
	}
}

type v4ByteStreamServer struct {
	bspb.UnimplementedByteStreamServer
}

func (*v4ByteStreamServer) Read(*bspb.ReadRequest, bspb.ByteStream_ReadServer) error { return nil }

// v4SocketTargets counts distinct socket inodes, excluding the transient FD
// used to enumerate /proc. This local helper supplements testleak's goroutines.
func v4SocketTargets(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	result := map[string]bool{}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err == nil && strings.HasPrefix(target, "socket:[") {
			result[target] = true
		}
	}
	return result
}
func TestV4ReachablePermittedTargets(t *testing.T) {
	// httptest supplies a test certificate valid for 127.0.0.1.
	certServer := httptest.NewTLSServer(nil)
	tlsConfig := certServer.TLS.Clone()
	cert := certServer.Certificate()
	certServer.Close()
	certFile := filepath.Join(t.TempDir(), "test-root.pem")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600))
	t.Setenv("SSL_CERT_FILE", certFile)
	for _, n := range []int{1, 10, 50} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			testleak.Check(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)))
			bspb.RegisterByteStreamServer(server, &v4ByteStreamServer{})
			go server.Serve(listener)
			t.Cleanup(server.Stop)
			env := real_environment.NewRealEnv(nil)
			flags.Set(t, "app.restrict_bytestream_dialing", true)
			flags.Set(t, "app.cache_api_url", url.URL{Scheme: "grpcs", Host: "127.0.0.1"})
			p := NewPooledByteStreamClient(env)
			t.Cleanup(func() { p.close(context.Background()) })
			before := v4SocketTargets(t)
			for i := range n {
				u, err := url.Parse(fmt.Sprintf("bytestream://synthetic-%d@%s/blobs/abc/1", i, listener.Addr()))
				require.NoError(t, err)
				require.True(t, isPermittedForDial(getTargetForURL(u, true)))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err = p.StreamBytestreamFile(ctx, u, io.Discard)
				cancel()
				require.NoError(t, err)
			}
			require.Eventually(t, func() bool { return len(v4SocketTargets(t))-len(before) >= 4*n }, 5*time.Second, 10*time.Millisecond)
			t.Logf("N=%d pools=%d retained_socket_delta=%d (client and server)", n, len(p.connPoolMap), len(v4SocketTargets(t))-len(before))
			require.NoError(t, p.close(context.Background()))
			require.Eventually(t, func() bool { return len(v4SocketTargets(t)) <= len(before) }, 5*time.Second, 10*time.Millisecond)
			t.Logf("N=%d sockets_after_owner_close=%d", n, len(v4SocketTargets(t))-len(before))

		})
	}
}
