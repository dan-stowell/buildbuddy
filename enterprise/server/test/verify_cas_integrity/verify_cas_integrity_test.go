package verify_cas_integrity_test

// Verifier edge-case matrix (verify-cas-integrity): valid ByteStream reads
// (offset/limit/compressor combinations) on pebble and memory caches, plus
// out-of-range offsets and wrong-size digests. Every case logs a
// "CASE ..." line so baseline and fix behaviour can be diffed. Valid
// requests must return OK with exactly the requested bytes.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/backends/pebble_cache"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/byte_stream_server"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/cachetools"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/content_addressable_storage_server"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testfs"
	"github.com/buildbuddy-io/buildbuddy/server/util/compression"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	gstatus "google.golang.org/grpc/status"
)

const df = repb.DigestFunction_SHA256

type env struct {
	ctx context.Context
	cas repb.ContentAddressableStorageClient
	bs  bspb.ByteStreamClient
}

func setup(t *testing.T, usePebble bool) *env {
	// Small threshold so the chunked-read fallback is exercised cheaply.
	flags.Set(t, "cache.min_chunked_read_fallback_size_bytes", 1024)
	te := testenv.GetTestEnv(t)
	if usePebble {
		pc, err := pebble_cache.NewPebbleCache(te, &pebble_cache.Options{
			RootDirectory:          testfs.MakeTempDir(t),
			MaxSizeBytes:           2_000_000_000,
			MaxInlineFileSizeBytes: 16384,
		})
		require.NoError(t, err)
		require.NoError(t, pc.Start())
		t.Cleanup(func() { pc.Stop() })
		te.SetCache(pc)
	}
	ctx, err := prefix.AttachUserPrefixToContext(context.Background(), te.GetAuthenticator())
	require.NoError(t, err)
	casServer, err := content_addressable_storage_server.NewContentAddressableStorageServer(te)
	require.NoError(t, err)
	bsServer, err := byte_stream_server.NewByteStreamServer(te)
	require.NoError(t, err)
	grpcServer, runFunc, lis := testenv.RegisterLocalGRPCServer(t, te)
	repb.RegisterContentAddressableStorageServer(grpcServer, casServer)
	bspb.RegisterByteStreamServer(grpcServer, bsServer)
	go runFunc()
	conn, err := testenv.LocalGRPCConn(t, ctx, lis, grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16*1024*1024)))
	require.NoError(t, err)
	return &env{ctx: ctx, cas: repb.NewContentAddressableStorageClient(conn), bs: bspb.NewByteStreamClient(conn)}
}

func (e *env) upload(t *testing.T, b []byte) *repb.Digest {
	d, err := cachetools.UploadBlob(e.ctx, e.bs, "", df, bytes.NewReader(b))
	require.NoError(t, err)
	return d
}

func (e *env) read(d *repb.Digest, c repb.Compressor_Value, offset, limit int64) (error, []byte) {
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	rn := digest.NewCASResourceName(d, "", df)
	rn.SetCompressor(c)
	stream, err := e.bs.Read(ctx, &bspb.ReadRequest{ResourceName: rn.DownloadString(), ReadOffset: offset, ReadLimit: limit})
	if err != nil {
		return err, nil
	}
	var buf bytes.Buffer
	for {
		rsp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err, buf.Bytes()
		}
		buf.Write(rsp.GetData())
	}
	data := buf.Bytes()
	if c == repb.Compressor_ZSTD {
		dec, err := compression.DecompressZstd(nil, data)
		if err != nil {
			return fmt.Errorf("client-side zstd decode: %w", err), data
		}
		data = dec
	}
	return nil, data
}

func randBlob(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + r.Intn(6))
	}
	return b
}

func TestVerifyReadMatrix(t *testing.T) {
	for _, usePebble := range []bool{true, false} {
		backend := "memory"
		if usePebble {
			backend = "pebble"
		}
		t.Run(backend, func(t *testing.T) {
			e := setup(t, usePebble)
			r := rand.New(rand.NewSource(42))
			type blobCase struct {
				name    string
				data    []byte
				d       *repb.Digest
				chunked bool
			}
			var blobs []blobCase
			for _, n := range []int{1, 50, 99, 100, 101, 5000, 100_000} {
				b := randBlob(r, n)
				blobs = append(blobs, blobCase{name: fmt.Sprintf("full_%d", n), data: b, d: e.upload(t, b)})
			}
			// Chunked-only blob (no full copy): 3 chunks, last one smaller.
			{
				c0, c1, c2 := randBlob(r, 40_000), randBlob(r, 40_000), randBlob(r, 12_345)
				b := append(append(append([]byte{}, c0...), c1...), c2...)
				bd, err := digest.Compute(bytes.NewReader(b), df)
				require.NoError(t, err)
				_, err = e.cas.SpliceBlob(e.ctx, &repb.SpliceBlobRequest{
					BlobDigest:     bd,
					ChunkDigests:   []*repb.Digest{e.upload(t, c0), e.upload(t, c1), e.upload(t, c2)},
					DigestFunction: df,
				})
				require.NoError(t, err)
				blobs = append(blobs, blobCase{name: "chunked_92345", data: b, d: bd, chunked: true})
			}

			for _, bc := range blobs {
				size := int64(len(bc.data))
				offsets := []int64{0, 1, size / 2, size - 1, size}
				limits := []int64{0, 1, size / 3, size + 10}
				for _, c := range []repb.Compressor_Value{repb.Compressor_IDENTITY, repb.Compressor_ZSTD} {
					for _, off := range offsets {
						for _, lim := range limits {
							if off < 0 {
								continue
							}
							err, got := e.read(bc.d, c, off, lim)
							end := size
							if lim > 0 && off+lim < end {
								end = off + lim
							}
							want := bc.data[off:end]
							match := bytes.Equal(got, want)
							t.Logf("CASE %s/%s/%s/off=%d/lim=%d code=%s len=%d match=%v", backend, bc.name, c, off, lim, gstatus.Code(err), len(got), match)
							if err == nil {
								require.True(t, match, "%s %s off=%d lim=%d: OK but wrong data (len %d, want %d)", bc.name, c, off, lim, len(got), len(want))
							} else if !bc.chunked || lim == 0 {
								// Valid request on a stored blob must succeed.
								t.Errorf("FAILVALID %s/%s/%s/off=%d/lim=%d: %v", backend, bc.name, c, off, lim, err)
							}
						}
					}
					// Informational: out-of-range offset and wrong-size digests.
					err, got := e.read(bc.d, c, size+5, 0)
					t.Logf("CASE %s/%s/%s/off=size+5 code=%s len=%d", backend, bc.name, c, gstatus.Code(err), len(got))
					for _, delta := range []int64{-1, +7} {
						wd := &repb.Digest{Hash: bc.d.GetHash(), SizeBytes: size + delta}
						if wd.SizeBytes < 0 {
							continue
						}
						err, got := e.read(wd, c, 0, 0)
						t.Logf("CASE %s/%s/%s/wrongsize%+d code=%s len=%d", backend, bc.name, c, delta, gstatus.Code(err), len(got))
					}
				}
				// cachetools.GetBlob (executor client) must work.
				var buf bytes.Buffer
				err := cachetools.GetBlob(e.ctx, e.bs, digest.NewCASResourceName(bc.d, "", df), &buf)
				t.Logf("CASE %s/%s/cachetools.GetBlob code=%s match=%v", backend, bc.name, gstatus.Code(err), bytes.Equal(buf.Bytes(), bc.data))
				require.NoError(t, err)
				require.Equal(t, bc.data, buf.Bytes())
			}
		})
	}
}
