package byte_stream_server

// Independent reproducer (repro-cas-integrity) for the claim that
// ByteStreamServer.Read returns OK with truncated data, or spins forever,
// when the cache reader fails with io.ErrUnexpectedEOF.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/compression"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	rspb "github.com/buildbuddy-io/buildbuddy/proto/resource"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	gstatus "google.golang.org/grpc/status"
)

// failingReader returns `prefix` and then `tailErr` forever.
type failingReader struct {
	data    []byte
	tailErr error
	// If oneShot, tailErr is returned once and io.EOF afterwards
	// (a non-sticky error); otherwise tailErr is returned forever.
	oneShot  bool
	reported bool
	reads    int
}

func (r *failingReader) Read(p []byte) (int, error) {
	r.reads++
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	if r.oneShot && r.reported {
		return 0, io.EOF
	}
	r.reported = true
	return 0, r.tailErr
}

type rawReadResult struct {
	data      []byte
	msgs      int
	emptyMsgs int
	err       error
	elapsed   time.Duration
}

func rawBSRead(ctx context.Context, client bspb.ByteStreamClient, resourceName string) rawReadResult {
	start := time.Now()
	res := rawReadResult{}
	stream, err := client.Read(ctx, &bspb.ReadRequest{ResourceName: resourceName})
	if err != nil {
		res.err = err
		res.elapsed = time.Since(start)
		return res
	}
	var buf bytes.Buffer
	for {
		rsp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			res.err = err
			break
		}
		res.msgs++
		if len(rsp.GetData()) == 0 {
			res.emptyMsgs++
		}
		buf.Write(rsp.GetData())
	}
	res.data = buf.Bytes()
	res.elapsed = time.Since(start)
	return res
}

func setupFaultyBS(t *testing.T, mkReader func() io.Reader) (context.Context, bspb.ByteStreamClient) {
	ctx := context.Background()
	te := testenv.GetTestEnv(t)
	ctx, err := prefix.AttachUserPrefixToContext(ctx, te.GetAuthenticator())
	require.NoError(t, err)
	te.SetCache(&readerFuncCache{
		Cache: te.GetCache(),
		reader: func(ctx context.Context, r *rspb.ResourceName, offset, limit int64) (io.ReadCloser, error) {
			return io.NopCloser(mkReader()), nil
		},
	})
	conn := runByteStreamServer(ctx, t, te)
	return ctx, bspb.NewByteStreamClient(conn)
}

func randomBlob(t *testing.T, seed int64, n int) ([]byte, *repb.Digest) {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	d, err := digest.Compute(bytes.NewReader(b), repb.DigestFunction_SHA256)
	require.NoError(t, err)
	return b, d
}

func resourceName(d *repb.Digest, c repb.Compressor_Value) string {
	rn := digest.NewCASResourceName(d, "", repb.DigestFunction_SHA256)
	rn.SetCompressor(c)
	return rn.DownloadString()
}

// (a) Reader returns some bytes, then io.ErrUnexpectedEOF. The client must
// get an error, not OK with a short blob.
func TestReproCAS_PartialThenErrUnexpectedEOF(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int
		goodBytes int
		oneShot   bool
	}{
		{"small_10000_half_sticky", 10000, 5000, false},
		{"small_10000_half_oneshot", 10000, 5000, true},
		{"large_3MB_multi_message_sticky", 3_000_000, 1_234_567, false},
		{"large_3MB_multi_message_oneshot", 3_000_000, 1_234_567, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blob, d := randomBlob(t, 1, tc.size)
			ctx, client := setupFaultyBS(t, func() io.Reader {
				return &failingReader{data: blob[:tc.goodBytes], tailErr: io.ErrUnexpectedEOF, oneShot: tc.oneShot}
			})
			for _, c := range []repb.Compressor_Value{repb.Compressor_IDENTITY, repb.Compressor_ZSTD} {
				rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
				res := rawBSRead(rctx, client, resourceName(d, c))
				cancel()
				data := res.data
				if c == repb.Compressor_ZSTD && res.err == nil {
					var derr error
					data, derr = compression.DecompressZstd(nil, res.data)
					t.Logf("zstd decompress err=%v", derr)
				}
				t.Logf("%s read of %d-byte blob (reader returns %d bytes then io.ErrUnexpectedEOF, oneShot=%v): err=%v code=%s bytes=%d msgs=%d empty=%d elapsed=%s",
					c, tc.size, tc.goodBytes, tc.oneShot, res.err, gstatus.Code(res.err), len(data), res.msgs, res.emptyMsgs, res.elapsed)
				assert.Error(t, res.err, "%s: client got status OK with %d of %d bytes", c, len(data), tc.size)
				assert.NotEqual(t, codes.DeadlineExceeded, gstatus.Code(res.err), "%s: server spun until client deadline (%d empty msgs)", c, res.emptyMsgs)
			}
		})
	}
}

// (a') Same, but the reader ends early with a plain io.EOF (e.g. a truncated
// file). This isolates "no length check" from "ErrUnexpectedEOF swallowed".
func TestReproCAS_PartialThenPlainEOF(t *testing.T) {
	blob, d := randomBlob(t, 2, 10000)
	ctx, client := setupFaultyBS(t, func() io.Reader {
		return &failingReader{data: blob[:5000], tailErr: io.EOF}
	})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res := rawBSRead(ctx, client, resourceName(d, repb.Compressor_IDENTITY))
	t.Logf("IDENTITY read, reader returns 5000 of 10000 bytes then io.EOF: err=%v bytes=%d msgs=%d", res.err, len(res.data), res.msgs)
	assert.Error(t, res.err, "client got status OK with %d of %d bytes", len(res.data), len(blob))
}

// Control: any other error mid-stream is propagated.
func TestReproCAS_PartialThenOtherErrorControl(t *testing.T) {
	blob, d := randomBlob(t, 3, 10000)
	ctx, client := setupFaultyBS(t, func() io.Reader {
		return &failingReader{data: blob[:5000], tailErr: status.UnavailableError("peer went away")}
	})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res := rawBSRead(ctx, client, resourceName(d, repb.Compressor_IDENTITY))
	t.Logf("control: err=%v bytes=%d msgs=%d", res.err, len(res.data), res.msgs)
	require.Error(t, res.err)
	assert.Equal(t, codes.Unavailable, gstatus.Code(res.err))
}

// (b) Reader returns (0, io.ErrUnexpectedEOF) forever. The server must
// terminate with an error quickly instead of streaming empty messages.
func TestReproCAS_ZeroUnexpectedEOFForever(t *testing.T) {
	_, d := randomBlob(t, 4, 10000)
	var lastReader *failingReader
	ctx, client := setupFaultyBS(t, func() io.Reader {
		lastReader = &failingReader{tailErr: io.ErrUnexpectedEOF}
		return lastReader
	})
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res := rawBSRead(ctx, client, resourceName(d, repb.Compressor_IDENTITY))
	t.Logf("(0, ErrUnexpectedEOF) forever: err=%v code=%s bytes=%d msgs=%d empty=%d elapsed=%s readerCalls=%d",
		res.err, gstatus.Code(res.err), len(res.data), res.msgs, res.emptyMsgs, res.elapsed, lastReader.reads)
	require.Error(t, res.err)
	assert.NotEqual(t, codes.DeadlineExceeded, gstatus.Code(res.err), "server never terminated the stream; client deadline hit")
	assert.LessOrEqual(t, res.emptyMsgs, 1, "server sent %d empty ReadResponses", res.emptyMsgs)
}

// (b') The real decompressor used by pebble (compression.NewZstdDecompressingReader)
// over a truncated stored zstd stream. This is what pebble's readerForMetadata does
// for an IDENTITY read of a blob that was stored zstd-compressed (minus the
// GCS/disk reader and the optional decryptor).
func TestReproCAS_TruncatedStoredZstdRealDecoder(t *testing.T) {
	// Compressible but non-trivial data.
	r := rand.New(rand.NewSource(5))
	blob := make([]byte, 1_000_000)
	for i := range blob {
		blob[i] = byte('a' + r.Intn(4))
	}
	d, err := digest.Compute(bytes.NewReader(blob), repb.DigestFunction_SHA256)
	require.NoError(t, err)
	compressed := compression.CompressZstd(nil, blob)
	t.Logf("blob %d bytes, compressed %d bytes", len(blob), len(compressed))

	for _, frac := range []float64{0.01, 0.25, 0.5, 0.9, 0.999} {
		cut := int(float64(len(compressed)) * frac)
		t.Run(fmt.Sprintf("cut_permille_%03d", int(frac*1000)), func(t *testing.T) {
			truncated := compressed[:cut]
			// First: direct behaviour of the decoder.
			dr, err := compression.NewZstdDecompressingReader(io.NopCloser(bytes.NewReader(truncated)))
			require.NoError(t, err)
			p := make([]byte, 64*1024)
			total := 0
			var lastErr error
			zeroErrReads := 0
			for range 1000 {
				n, err := dr.Read(p)
				total += n
				lastErr = err
				if n == 0 && err != nil && err != io.EOF {
					zeroErrReads++
				}
				if err == io.EOF {
					break
				}
			}
			dr.Close()
			t.Logf("cut=%d/%d: decoder direct: total=%d lastErr=%v (isUnexpectedEOF=%v) zeroByteErrReads(of max 1000)=%d",
				cut, len(compressed), total, lastErr, errors.Is(lastErr, io.ErrUnexpectedEOF), zeroErrReads)

			ctx, client := setupFaultyBS(t, func() io.Reader {
				dr, err := compression.NewZstdDecompressingReader(io.NopCloser(bytes.NewReader(truncated)))
				if err != nil {
					panic(err)
				}
				return dr
			})
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			res := rawBSRead(ctx, client, resourceName(d, repb.Compressor_IDENTITY))
			t.Logf("cut=%d/%d: ByteStream IDENTITY read: err=%v code=%s bytes=%d/%d msgs=%d empty=%d elapsed=%s",
				cut, len(compressed), res.err, gstatus.Code(res.err), len(res.data), len(blob), res.msgs, res.emptyMsgs, res.elapsed)
			require.Error(t, res.err, "OK with %d/%d bytes", len(res.data), len(blob))
			assert.NotEqual(t, codes.DeadlineExceeded, gstatus.Code(res.err), "server spun until client deadline (%d empty msgs)", res.emptyMsgs)
		})
	}
}
