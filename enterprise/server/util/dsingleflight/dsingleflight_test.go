package dsingleflight

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testredis"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

type workResult struct {
	data []byte
	err  error
}

func TestDo(t *testing.T) {
	numWorkers := 20
	redisServer := testredis.Start(t)
	c := newTestCoordinator(t, redisServer, numWorkers)
	ctx := context.Background()

	key := fmt.Sprintf("key-%d", rand.Int())

	wg, ctx := errgroup.WithContext(ctx)

	results := make(chan *workResult, numWorkers)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()

	var mu sync.Mutex
	numExecutions := 0
	usedWorker := ""

	for i := range numWorkers {
		worker := fmt.Sprintf("worker_%d", i)
		wg.Go(func() error {
			res, err := c.Do(ctx, key, func() ([]byte, error) {
				mu.Lock()
				numExecutions++
				usedWorker = worker
				mu.Unlock()
				<-release
				return []byte(worker), nil
			})
			results <- &workResult{res, err}
			return nil
		})
	}

	waitForResultReaders(t, redisServer, numWorkers)
	unblock()

	err := wg.Wait()
	require.NoError(t, err)

	require.Equal(t, 1, numExecutions, "expected work to be done only once")
	// all callers should have gotten the same result
	close(results)
	for result := range results {
		require.NoError(t, result.err)
		require.Equal(t, usedWorker, string(result.data))
	}
}

func TestDoError(t *testing.T) {
	numWorkers := 20
	redisServer := testredis.Start(t)
	c := newTestCoordinator(t, redisServer, numWorkers)
	ctx := context.Background()

	key := fmt.Sprintf("key-%d", rand.Int())

	wg, ctx := errgroup.WithContext(ctx)

	results := make(chan *workResult, numWorkers)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()

	var mu sync.Mutex
	numExecutions := 0

	for range numWorkers {
		wg.Go(func() error {
			res, err := c.Do(ctx, key, func() ([]byte, error) {
				mu.Lock()
				numExecutions++
				mu.Unlock()
				<-release
				return nil, status.UnavailableError("out of puppies")
			})
			results <- &workResult{res, err}
			return nil
		})
	}

	waitForResultReaders(t, redisServer, numWorkers)
	unblock()

	err := wg.Wait()
	require.NoError(t, err)

	require.Equal(t, 1, numExecutions, "expected work to be done only once")
	// all callers should have gotten the same result
	close(results)
	for result := range results {
		require.ErrorContains(t, result.err, "out of puppies")
		require.True(t, status.IsUnavailableError(result.err))
	}
}

// newTestCoordinator reserves a connection for coordinator commands in addition
// to one blocking result read per worker, even when GOMAXPROCS is 1.
func newTestCoordinator(t *testing.T, redisServer *testredis.Handle, numWorkers int) *Coordinator {
	t.Helper()
	options := *redisServer.Client().(*redis.Client).Options()
	options.PoolSize = numWorkers + 1
	rdb := redis.NewClient(&options)
	t.Cleanup(func() { rdb.Close() })
	return New(rdb)
}

// waitForResultReaders waits until Redis has observed every caller blocked on
// XREAD, including the leader's result reader. Each test has its own Redis
// server, so these are all readers for the test's work key. Keep the callback
// behind the release barrier until this condition holds.
func waitForResultReaders(t *testing.T, redisServer *testredis.Handle, numWorkers int) {
	t.Helper()
	// Use a separate connection pool: the workers' pool contains blocking reads.
	rdb := redisServer.Client()
	require.Eventually(t, func() bool {
		clients, err := rdb.ClientList(context.Background()).Result()
		require.NoError(t, err)
		readers := 0
		for client := range strings.SplitSeq(clients, "\n") {
			blocked, reading := false, false
			for field := range strings.FieldsSeq(client) {
				if flags, ok := strings.CutPrefix(field, "flags="); ok {
					blocked = strings.Contains(flags, "b")
				}
				if field == "cmd=xread" {
					reading = true
				}
			}
			if blocked && reading {
				readers++
			}
		}
		return readers == numWorkers
	}, 10*time.Second, time.Millisecond, "all callers should be waiting for the result before releasing work")
}
