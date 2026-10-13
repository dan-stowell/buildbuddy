package memory_kvstore_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/backends/memory_kvstore"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/require"
)

func TestReplaceSuffix(t *testing.T) {
	ctx := t.Context()
	store, err := memory_kvstore.NewMemoryKeyValStore()
	require.NoError(t, err)

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
	store, err := memory_kvstore.NewMemoryKeyValStore()
	require.NoError(t, err)

	// Writing a suffix to a key that doesn't exist (for example, because it
	// was evicted) should fail with FailedPrecondition and not create the key.
	err = store.ReplaceSuffix(ctx, "missing", 5, 0, []byte("data"))
	require.True(t, status.IsFailedPreconditionError(err), "expected FailedPrecondition, got %v", err)
	_, err = store.Get(ctx, "missing")
	require.True(t, status.IsNotFoundError(err), "expected NotFound, got %v", err)
}

func TestCapacityAndEviction(t *testing.T) {
	ctx := t.Context()
	store, err := memory_kvstore.NewMemoryKeyValStore()
	require.NoError(t, err)
	// Values of different lengths, including empty slices, each count as one entry.
	for i := range 5000 {
		require.NoError(t, store.Set(ctx, fmt.Sprint(i), make([]byte, i%100)))
	}
	for i := range 5000 {
		val, err := store.Get(ctx, fmt.Sprint(i))
		require.NoError(t, err)
		require.Len(t, val, i%100)
	}
	// Reads and updates both refresh recency; updating does not use another slot.
	_, err = store.Get(ctx, "0")
	require.NoError(t, err)
	require.NoError(t, store.Set(ctx, "1", []byte("updated")))
	require.NoError(t, store.Set(ctx, "overflow", []byte{}))
	_, err = store.Get(ctx, "2")
	require.True(t, status.IsNotFoundError(err))
	for _, key := range []string{"0", "1", "3", "overflow"} {
		_, err = store.Get(ctx, key)
		require.NoError(t, err)
	}
	// Deleting frees a slot without evicting another entry.
	require.NoError(t, store.Set(ctx, "overflow", nil))
	_, err = store.Get(ctx, "overflow")
	require.True(t, status.IsNotFoundError(err))
	require.NoError(t, store.Set(ctx, "replacement", []byte{}))
	_, err = store.Get(ctx, "4")
	require.NoError(t, err)
}

func TestConcurrentAccess(t *testing.T) {
	ctx := t.Context()
	store, err := memory_kvstore.NewMemoryKeyValStore()
	require.NoError(t, err)
	var wg sync.WaitGroup
	for worker := range 16 {
		wg.Go(func() {
			// Each worker owns its values; shared keys exercise LRU locking without
			// mutating slices returned to other callers.
			key := fmt.Sprintf("worker-%d", worker)
			for range 100 {
				if err := store.Set(ctx, key, []byte("a")); err != nil {
					t.Error(err)
					return
				}
				if err := store.ReplaceSuffix(ctx, key, 1, 1, []byte("b")); err != nil {
					t.Error(err)
					return
				}
				val, err := store.Get(ctx, key)
				if err != nil || string(val) != "ab" {
					t.Errorf("Get = %q, %v", val, err)
					return
				}
				if err := store.Set(ctx, key, nil); err != nil {
					t.Error(err)
					return
				}
				if err := store.Set(ctx, "shared", []byte("value")); err != nil {
					t.Error(err)
					return
				}
				if _, err := store.Get(ctx, "shared"); err != nil && !status.IsNotFoundError(err) {
					t.Error(err)
					return
				}
				if err := store.Set(ctx, "shared", nil); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
}
