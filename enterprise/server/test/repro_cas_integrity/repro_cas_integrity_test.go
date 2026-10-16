package repro_cas_integrity_test

// Independent end-to-end reproducer (repro-cas-integrity): SpliceBlob with
// wrong chunk sizes after a valid splice, on a real pebble cache with
// cache.chunking.ac_key_salt set, then read the blob back via every read API.
// Uses the production default chunked-read fallback threshold (2MB).

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
	"github.com/stretchr/testify/assert"
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

func setup(t *testing.T, salt string) *env {
	if salt != "" {
		flags.Set(t, "cache.chunking.ac_key_salt", salt)
	}
	te := testenv.GetTestEnv(t)
	pc, err := pebble_cache.NewPebbleCache(te, &pebble_cache.Options{
		RootDirectory:          testfs.MakeTempDir(t),
		MaxSizeBytes:           2_000_000_000,
		MaxInlineFileSizeBytes: 16384,
	})
	require.NoError(t, err)
	require.NoError(t, pc.Start())
	t.Cleanup(func() { pc.Stop() })
	te.SetCache(pc)

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

func dig(t *testing.T, b []byte) *repb.Digest {
	d, err := digest.Compute(bytes.NewReader(b), df)
	require.NoError(t, err)
	return d
}

func (e *env) upload(t *testing.T, b []byte) *repb.Digest {
	d, err := cachetools.UploadBlob(e.ctx, e.bs, "", df, bytes.NewReader(b))
	require.NoError(t, err)
	return d
}

func (e *env) splice(blob *repb.Digest, chunks ...*repb.Digest) error {
	_, err := e.cas.SpliceBlob(e.ctx, &repb.SpliceBlobRequest{
		BlobDigest:     blob,
		ChunkDigests:   chunks,
		DigestFunction: df,
	})
	return err
}

type readOutcome struct {
	err  error
	data []byte
	msgs int
}

func (e *env) rawRead(d *repb.Digest, c repb.Compressor_Value, offset int64) readOutcome {
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	rn := digest.NewCASResourceName(d, "", df)
	rn.SetCompressor(c)
	stream, err := e.bs.Read(ctx, &bspb.ReadRequest{ResourceName: rn.DownloadString(), ReadOffset: offset})
	if err != nil {
		return readOutcome{err: err}
	}
	var buf bytes.Buffer
	o := readOutcome{}
	for {
		rsp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			o.err = err
			break
		}
		o.msgs++
		buf.Write(rsp.GetData())
	}
	o.data = buf.Bytes()
	if c == repb.Compressor_ZSTD && o.err == nil {
		dec, derr := compression.DecompressZstd(nil, o.data)
		if derr != nil {
			o.err = derr
		}
		o.data = dec
	}
	return o
}

func describe(blob []byte, o readOutcome) string {
	verdict := "MATCH"
	if o.err != nil {
		verdict = "ERROR"
	} else if !bytes.Equal(o.data, blob) {
		verdict = "OK-BUT-WRONG-CONTENT"
	}
	return fmt.Sprintf("%s err=%s len=%d/%d", verdict, errString(o.err), len(o.data), len(blob))
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return gstatus.Code(err).String() + ": " + err.Error()
}

// readAll reads the blob through every read API and reports whether any read
// returned OK with content that does not match the digest.
func (e *env) readAll(t *testing.T, blob []byte, d *repb.Digest) (okButWrong []string) {
	check := func(name string, o readOutcome) {
		t.Logf("  %-34s %s", name, describe(blob, o))
		if o.err == nil && !bytes.Equal(o.data, blob) {
			okButWrong = append(okButWrong, name)
		}
	}
	check("ByteStream.Read IDENTITY", e.rawRead(d, repb.Compressor_IDENTITY, 0))
	check("ByteStream.Read ZSTD", e.rawRead(d, repb.Compressor_ZSTD, 0))
	off := e.rawRead(d, repb.Compressor_IDENTITY, 100)
	if off.err == nil {
		off.data = append(append([]byte{}, blob[:100]...), off.data...)
	}
	check("ByteStream.Read IDENTITY offset=100", off)

	for _, c := range []repb.Compressor_Value{repb.Compressor_IDENTITY, repb.Compressor_ZSTD} {
		req := &repb.BatchReadBlobsRequest{Digests: []*repb.Digest{d}, DigestFunction: df}
		if c == repb.Compressor_ZSTD {
			req.AcceptableCompressors = []repb.Compressor_Value{repb.Compressor_ZSTD}
		}
		rsp, err := e.cas.BatchReadBlobs(e.ctx, req)
		o := readOutcome{err: err}
		if err == nil {
			r := rsp.GetResponses()[0]
			if r.GetStatus().GetCode() != 0 {
				o.err = gstatus.ErrorProto(r.GetStatus())
			} else {
				o.data = r.GetData()
				if r.GetCompressor() == repb.Compressor_ZSTD {
					o.data, o.err = compression.DecompressZstd(nil, o.data)
				}
			}
		}
		check("BatchReadBlobs "+c.String(), o)
	}

	var buf bytes.Buffer
	err := cachetools.GetBlob(e.ctx, e.bs, digest.NewCASResourceName(d, "", df), &buf)
	check("cachetools.GetBlob (client verifies)", readOutcome{err: err, data: buf.Bytes()})

	sp, err := e.cas.SplitBlob(e.ctx, &repb.SplitBlobRequest{BlobDigest: d, DigestFunction: df})
	if err == nil {
		var sizes []int64
		var total int64
		for _, c := range sp.GetChunkDigests() {
			sizes = append(sizes, c.GetSizeBytes())
			total += c.GetSizeBytes()
		}
		t.Logf("  %-34s chunk sizes=%v total=%d (blob %d)", "SplitBlob", sizes, total, len(blob))
	} else {
		t.Logf("  %-34s err=%s", "SplitBlob", errString(err))
	}
	return okButWrong
}

func TestReproSpliceBadSizesServesWrongContent(t *testing.T) {
	const c0Size, c1Size = 1_500_000, 1_000_000 // blob 2.5MB > 2MB prod chunked-read fallback threshold
	for _, tc := range []struct {
		name       string
		salt       string
		validFirst bool
		d0, d1     int64 // delta applied to declared chunk sizes in the bad splice
	}{
		{name: "salt_valid_first_grow_last_chunk", salt: "s", validFirst: true, d1: +1},
		{name: "salt_valid_first_shrink_last_chunk", salt: "s", validFirst: true, d1: -1},
		{name: "salt_valid_first_move_byte_c0_to_c1", salt: "s", validFirst: true, d0: -1, d1: +1},
		{name: "salt_valid_first_move_byte_c1_to_c0", salt: "s", validFirst: true, d0: +1, d1: -1},
		{name: "salt_no_valid_first_grow_last_chunk", salt: "s", validFirst: false, d1: +1},
		{name: "nosalt_valid_first_grow_last_chunk", salt: "", validFirst: true, d1: +1},
		{name: "nosalt_valid_first_move_byte_c1_to_c0", salt: "", validFirst: true, d0: +1, d1: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t, tc.salt)
			r := rand.New(rand.NewSource(42))
			c0 := make([]byte, c0Size)
			c1 := make([]byte, c1Size)
			r.Read(c0)
			r.Read(c1)
			blob := append(append([]byte{}, c0...), c1...)
			bd := dig(t, blob)
			d0 := e.upload(t, c0)
			d1 := e.upload(t, c1)

			if tc.validFirst {
				require.NoError(t, e.splice(bd, d0, d1))
				t.Logf("after VALID splice:")
				require.Empty(t, e.readAll(t, blob, bd), "valid splice must read back correctly")
			}

			bad0 := &repb.Digest{Hash: d0.GetHash(), SizeBytes: d0.GetSizeBytes() + tc.d0}
			bad1 := &repb.Digest{Hash: d1.GetHash(), SizeBytes: d1.GetSizeBytes() + tc.d1}
			err := e.splice(bd, bad0, bad1)
			t.Logf("BAD splice sizes [%d, %d] for blob size %d: err=%s", bad0.GetSizeBytes(), bad1.GetSizeBytes(), bd.GetSizeBytes(), errString(err))
			t.Logf("after BAD splice:")
			wrong := e.readAll(t, blob, bd)
			assert.Empty(t, wrong, "reads returned status OK with content that does not match the digest")
		})
	}
}
