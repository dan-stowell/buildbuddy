package redis_kvstore_test

import (
	"fmt"
	elpb "github.com/buildbuddy-io/buildbuddy/proto/eventlog"
	inspb "github.com/buildbuddy-io/buildbuddy/proto/invocation_status"
	"github.com/buildbuddy-io/buildbuddy/server/backends/chunkstore"
	"github.com/buildbuddy-io/buildbuddy/server/eventlog"
	"github.com/buildbuddy-io/buildbuddy/server/tables"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/mockinvocationdb"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/mockstore"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"math/rand"
	"strings"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/backends/redis_kvstore"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testredis"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/require"
)

func TestReplaceSuffix(t *testing.T) {
	ctx := t.Context()
	store := redis_kvstore.New(testredis.Start(t).Client())

	// Write an initial value.
	require.NoError(t, store.Set(ctx, "key", []byte("hello")))

	// Writing a suffix at the end of the value should append to it.
	require.NoError(t, store.ReplaceSuffix(ctx, "key", 5, 5, []byte(" world")))
	val, err := store.Get(ctx, "key")
	require.NoError(t, err)
	require.Equal(t, "hello world", string(val))

	// Writing a suffix at an offset within the value should overwrite the
	// bytes from that offset onward.
	require.NoError(t, store.ReplaceSuffix(ctx, "key", 11, 6, []byte("gopher")))
	val, err = store.Get(ctx, "key")
	require.NoError(t, err)
	require.Equal(t, "hello gopher", string(val))

	// Writing a shorter suffix should truncate the value.
	require.NoError(t, store.ReplaceSuffix(ctx, "key", 12, 6, []byte("go")))
	val, err = store.Get(ctx, "key")
	require.NoError(t, err)
	require.Equal(t, "hello go", string(val))

	// Writing a suffix with a stale expected length should fail with
	// FailedPrecondition and leave the value unchanged.
	err = store.ReplaceSuffix(ctx, "key", 5, 5, []byte("!!!"))
	require.True(t, status.IsFailedPreconditionError(err), "expected FailedPrecondition, got %v", err)
	val, err = store.Get(ctx, "key")
	require.NoError(t, err)
	require.Equal(t, "hello go", string(val))

	// An offset past the end of the value is invalid.
	err = store.ReplaceSuffix(ctx, "key", 8, 9, []byte("x"))
	require.True(t, status.IsInvalidArgumentError(err), "expected InvalidArgument, got %v", err)

	// When the expected length is stale and the offset is also out of range,
	// the offset is validated first, so the call should fail with
	// InvalidArgument rather than FailedPrecondition.
	err = store.ReplaceSuffix(ctx, "key", 5, 6, []byte("x"))
	require.True(t, status.IsInvalidArgumentError(err), "expected InvalidArgument, got %v", err)
}

func TestReplaceSuffix_MissingKey(t *testing.T) {
	ctx := t.Context()
	store := redis_kvstore.New(testredis.Start(t).Client())

	// Writing a suffix to a key that doesn't exist (for example, because it
	// was evicted) should fail with FailedPrecondition and not create the key.
	err := store.ReplaceSuffix(ctx, "missing", 5, 0, []byte("data"))
	require.True(t, status.IsFailedPreconditionError(err), "expected FailedPrecondition, got %v", err)
	_, err = store.Get(ctx, "missing")
	require.True(t, status.IsNotFoundError(err), "expected NotFound, got %v", err)
}

// Model the binary suffix operations used by live log chunks, including
// truncation and offsets inside UTF-8/ANSI bytes (Redis offsets are byte offsets).
func TestBuildLogsSuffixModel(t *testing.T) {
	const seed = int64(6460001)
	t.Logf("seed=%d", seed)
	rng := rand.New(rand.NewSource(seed))
	ctx := t.Context()
	store := redis_kvstore.New(testredis.Start(t).Client())
	model := []byte("0000hello 世界\x1b[31m\r\n")
	require.NoError(t, store.Set(ctx, "log", model))
	for i := range 300 {
		offset := rng.Intn(len(model) + 1)
		suffix := make([]byte, rng.Intn(256))
		_, err := rng.Read(suffix)
		require.NoError(t, err)
		require.NoError(t, store.ReplaceSuffix(ctx, "log", int64(len(model)), int64(offset), suffix), "iteration=%d", i)
		model = append(model[:offset:offset], suffix...)
		got, err := store.Get(ctx, "log")
		require.NoError(t, err)
		require.Equal(t, model, got, "iteration=%d", i)
	}
}

// A live response is a replacement snapshot of its chunk. Only persisted
// responses advance the durable prefix; appending every live response is invalid.
func TestBuildLogsRedisBoundaryPolling(t *testing.T) {
	for _, size := range []int{1_999_999, 2_000_000, 2_000_001} {
		for _, minLines := range []int32{0, 1000000} {
			t.Run(fmt.Sprintf("size=%d/min_lines=%d", size, minLines), func(t *testing.T) {
				ctx := t.Context()
				env := testenv.GetTestEnv(t)
				kv := redis_kvstore.New(testredis.Start(t).Client())
				env.SetKeyValStore(kv)
				env.SetBlobstore(mockstore.New())
				inv := &tables.Invocation{InvocationID: "boundary", Attempt: 1, LastChunkId: eventlog.EmptyId, InvocationStatus: int64(inspb.InvocationStatus_PARTIAL_INVOCATION_STATUS)}
				env.SetInvocationDB(&mockinvocationdb.MockInvocationDB{DB: map[string]*tables.Invocation{"boundary": inv}})
				path := eventlog.GetEventLogPathFromInvocationIdAndAttempt("boundary", 1)
				w, err := eventlog.NewEventLogWriter(ctx, env.GetBlobstore(), kv, nil, "", path, 2000, 2)
				require.NoError(t, err)
				defer w.Close(ctx)
				// Short lines avoid terminal wrapping. The second event crosses the flush
				// boundary; an empty event exercises the no-op write path.
				first := strings.Repeat("0123456789abcdef\n", size/17) + strings.Repeat("x", size%17)
				events := []string{first, "\n" + strings.Repeat("next\n", 20), "", "final tail"}
				var sent, durable string
				next := "0000"
				poll := func() string {
					for range 10 {
						rsp, err := eventlog.GetEventLogChunk(ctx, env, &elpb.GetEventLogChunkRequest{InvocationId: "boundary", ChunkId: next, MinLines: minLines})
						require.NoError(t, err)
						if rsp.Live {
							require.Equal(t, next, rsp.NextChunkId)
							return durable + string(rsp.Buffer)
						}
						durable += string(rsp.Buffer)
						if rsp.NextChunkId == "" || rsp.NextChunkId == next {
							return durable
						}
						next = rsp.NextChunkId
					}
					t.Fatal("poll failed to reach live chunk or end")
					return ""
				}
				for _, e := range events {
					_, err := w.Write(ctx, []byte(e))
					require.NoError(t, err)
					// GetLastChunkId synchronizes with the writer loop and its write hook.
					inv.LastChunkId = w.GetLastChunkId(ctx)
					sent += e
					require.Equal(t, sent, poll())
					require.Equal(t, sent, poll(), "unchanged poll must not duplicate data")
					if len(sent) > size {
						require.NotEqual(t, eventlog.EmptyId, inv.LastChunkId)
					}
				}
				require.NoError(t, w.Close(ctx))
				inv.LastChunkId = w.GetLastChunkId(ctx)
				inv.InvocationStatus = int64(inspb.InvocationStatus_COMPLETE_INVOCATION_STATUS)
				require.Equal(t, sent, poll(), "completion must deliver the final tail")
				require.Equal(t, sent, durable)
				actualLast, err := chunkstore.New(env.GetBlobstore(), &chunkstore.ChunkstoreOptions{}).GetLastChunkId(ctx, path, "0000")
				require.NoError(t, err)
				last, err := eventlog.GetEventLogChunk(ctx, env, &elpb.GetEventLogChunkRequest{InvocationId: "boundary", ChunkId: actualLast})
				require.NoError(t, err)
				require.NotEmpty(t, last.Buffer)
				beyond, err := eventlog.GetEventLogChunk(ctx, env, &elpb.GetEventLogChunkRequest{InvocationId: "boundary", ChunkId: last.NextChunkId})
				require.NoError(t, err)
				require.Empty(t, beyond.Buffer)
				require.Empty(t, beyond.NextChunkId)
				require.Equal(t, actualLast, beyond.PreviousChunkId)
			})
		}
	}
}
