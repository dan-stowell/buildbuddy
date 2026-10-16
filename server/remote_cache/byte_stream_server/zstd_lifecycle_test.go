package byte_stream_server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testcompression"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testdigest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/compression"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
	bspb "google.golang.org/genproto/googleapis/bytestream"
)

// Use the real server and testenv cache, with deterministic transport failures.
// Returning Canceled from Recv after the first request ensures the server has
// actually acquired its decoder before cancellation; an RPC Send alone doesn't.
// Handlers are called synchronously below: no gRPC worker pool can hide them.
func TestZstdServerLifecycleGrowth(t *testing.T) {
	flags.Set(t, "grpc_server_worker_multiplier", 0)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("cache_zstd=%t", passthrough), func(t *testing.T) {
			testleak.Check(t)
			testleak.CheckFDs(t)
			te := testenv.GetTestEnv(t)
			flags.Set(t, "cache.zstd_transcoding_enabled", true)
			flags.Set(t, "cache.max_direct_write_size_bytes", 1)
			if passthrough {
				te.SetCache(&testcompression.CompressionCache{Cache: te.GetCache()})
			}
			server, err := NewByteStreamServer(te)
			require.NoError(t, err)
			rn, src := testdigest.RandomCASResourceBuf(t, 256*1024)
			compressed := compression.CompressZstd(nil, src)
			ctx, err := prefix.AttachUserPrefixToContext(context.Background(), te.GetAuthenticator())
			require.NoError(t, err)
			require.NoError(t, te.GetCache().Set(ctx, rn, src))
			for _, mode := range []string{"write_corrupt", "write_cancel", "read_cancel"} {
				t.Run(mode, func(t *testing.T) {
					for _, n := range []int{10, 50, 200} {
						t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
							testleak.CheckGrowth(t, testleak.Growth{Iterations: n}, func(i int) {
								if mode == "read_cancel" {
									stream := &canceledZstdReadStream{ctx: ctx}
									err := server.Read(&bspb.ReadRequest{ResourceName: fmt.Sprintf("compressed-blobs/zstd/%s/%d", rn.GetDigest().GetHash(), len(src))}, stream)
									require.ErrorIs(t, err, context.Canceled)
									require.Positive(t, stream.sends)
									return
								}
								data := []byte("not a zstd frame")
								if mode == "write_cancel" {
									data = compressed[:len(compressed)/2]
								}
								stream := &canceledZstdWriteStream{}
								stream.ctx = ctx
								stream.reqs = []*bspb.WriteRequest{{
									ResourceName: fmt.Sprintf("audit-%s-%d-%d/uploads/%s/compressed-blobs/zstd/%s/%d", mode, n, i, newUUID(t), strings.Repeat("b", 64), len(src)),
									Data:         data, FinishWrite: mode == "write_corrupt",
								}}
								require.Error(t, server.Write(stream))
								if mode == "write_cancel" {
									require.True(t, stream.canceled, "must reach second Recv with a live decoder")
								}
							})
						})
					}
				})
			}
		})
	}
}

type canceledZstdWriteStream struct {
	testWriteStream
	canceled bool
}

func (s *canceledZstdWriteStream) Recv() (*bspb.WriteRequest, error) {
	if len(s.reqs) > 0 {
		return s.testWriteStream.Recv()
	}
	s.canceled = true
	return nil, context.Canceled
}

type canceledZstdReadStream struct {
	bspb.ByteStream_ReadServer
	ctx   context.Context
	sends int
}

func (s *canceledZstdReadStream) Context() context.Context      { return s.ctx }
func (s *canceledZstdReadStream) Send(*bspb.ReadResponse) error { s.sends++; return context.Canceled }
