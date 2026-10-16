package compression_test

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os"
	"runtime"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
	"github.com/buildbuddy-io/buildbuddy/server/util/compression"
	"github.com/buildbuddy-io/buildbuddy/server/util/ioutil"
	"github.com/stretchr/testify/require"
)

// Exercise resources after their owner has closed, including real input FDs.
// Pools are warmed before measuring; no goroutines are ignored.
func TestZstdLifecycleGrowth(t *testing.T) {
	src := make([]byte, 256*1024)
	_, err := rand.New(rand.NewSource(1)).Read(src)
	require.NoError(t, err)
	compressed := compression.CompressZstd(nil, src)
	path := t.TempDir() + "/input.zstd"
	require.NoError(t, os.WriteFile(path, compressed, 0600))
	for _, mode := range []string{"reader_eof", "reader_partial", "reader_corrupt", "reader_source_error", "reader_sink_error", "batch_corrupt", "pipe_eof", "pipe_truncated", "pipe_corrupt", "pipe_sink_error", "writer_sink_error", "writer_abort", "compress_reader_eof", "compress_reader_partial", "compress_reader_source_error"} {
		t.Run(mode, func(t *testing.T) {
			op := func(_ int) {
				switch mode {
				case "reader_eof", "reader_partial", "reader_corrupt", "reader_source_error", "reader_sink_error":
					f, err := os.Open(path)
					require.NoError(t, err)
					var input io.ReadCloser = f
					if mode == "reader_corrupt" {
						input = &ownedReader{Reader: bytes.NewReader([]byte("invalid zstd")), Closer: f}
					}
					if mode == "reader_source_error" {
						input = &ownedReader{Reader: io.MultiReader(io.LimitReader(f, 16), failedReader{}), Closer: f}
					}
					r, err := compression.NewZstdDecompressingReader(input)
					require.NoError(t, err)
					switch mode {
					case "reader_partial":
						_, err = io.ReadFull(r, make([]byte, 1))
						require.NoError(t, err)
					case "reader_sink_error":
						_, err = io.Copy(&erroringWriter{}, r)
						require.Error(t, err)
					default:
						_, err = io.Copy(io.Discard, r)
						if mode == "reader_eof" {
							require.NoError(t, err)
						} else {
							require.Error(t, err)
						}
					}
					require.NoError(t, r.Close())
					require.NoError(t, r.Close())
					_, err = f.Read(make([]byte, 1))
					require.ErrorIs(t, err, os.ErrClosed)
				case "compress_reader_eof", "compress_reader_partial", "compress_reader_source_error":
					f, err := os.Open(path)
					require.NoError(t, err)
					var input io.ReadCloser = f
					if mode == "compress_reader_source_error" {
						input = &ownedReader{Reader: failedReader{}, Closer: f}
					}
					r, err := compression.NewZstdCompressingReader(input, bufPool, 1024)
					require.NoError(t, err)
					if mode == "compress_reader_partial" {
						_, err = io.ReadFull(r, make([]byte, 1))
						require.NoError(t, err)
					} else {
						_, err = io.Copy(io.Discard, r)
						if mode == "compress_reader_eof" {
							require.NoError(t, err)
						} else {
							require.Error(t, err)
						}
					}
					require.NoError(t, r.Close())
					require.NoError(t, r.Close())
					_, err = f.Read(make([]byte, 1))
					require.ErrorIs(t, err, os.ErrClosed)
				case "batch_corrupt":
					_, err := compression.DecompressZstd(nil, []byte("invalid zstd"))
					require.Error(t, err)
				case "pipe_eof", "pipe_truncated", "pipe_corrupt", "pipe_sink_error":
					var sink io.Writer = io.Discard
					if mode == "pipe_sink_error" {
						sink = &erroringWriter{}
					}
					d, err := compression.NewZstdDecompressor(sink)
					require.NoError(t, err)
					data := compressed
					if mode == "pipe_truncated" {
						data = compressed[:len(compressed)/2]
					}
					if mode == "pipe_corrupt" {
						data = []byte("invalid zstd")
					}
					_, writeErr := d.Write(data)
					closeErr := d.Close()
					if mode == "pipe_eof" {
						require.NoError(t, writeErr)
						require.NoError(t, closeErr)
					} else {
						require.True(t, writeErr != nil || closeErr != nil)
					}
					require.NoError(t, d.Close())
				case "writer_sink_error":
					w, err := compression.NewZstdCompressingWriter(&erroringWriter{}, bufPool, 1024)
					require.NoError(t, err)
					_, err = w.Write(src)
					require.Error(t, err)
					require.Error(t, w.Close())
				case "writer_abort":
					wc := ioutil.NewCustomCommitWriteCloser(io.Discard)
					w, err := compression.NewZstdCompressingWriteCommiter(wc, bufPool, 1024, "audit")
					require.NoError(t, err)
					_, err = w.Write(src[:1])
					require.NoError(t, err)
					require.NoError(t, w.Close())
				}
			}
			op(0)
			testleak.Check(t)
			testleak.CheckFDs(t)
			for _, n := range []int{10, 50, 200} {
				t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
					before := runtime.NumGoroutine()
					testleak.CheckGrowth(t, testleak.Growth{Iterations: n}, op)
					t.Logf("N=%d: goroutines before=%d after=%d; CheckGrowth passed with zero allowed new goroutines and FDs", n, before, runtime.NumGoroutine())
				})
			}
		})
	}
}

type ownedReader struct {
	io.Reader
	io.Closer
}
type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
