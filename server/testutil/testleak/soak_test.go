package testleak

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSoakGrowing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		series []int
		want   bool
	}{
		// Caught.
		{"one per batch", []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, true},
		{"many per batch", []int{34, 51, 68, 85, 102, 119, 136, 153, 170, 187, 204}, true},
		{"one dip", []int{10, 11, 12, 13, 14, 12, 16, 17, 18, 19, 20}, true},
		{"noisy", []int{18, 23, 27, 29, 31, 27, 31, 31, 36, 41, 40}, true},
		{"noisy end", []int{20, 22, 22, 22, 23, 25, 25, 27, 28, 30, 29}, true},
		{"bursts every third batch", []int{0, 0, 0, 3, 3, 3, 6, 6, 6, 9, 9}, true},
		// Accepted.
		{"flat", []int{5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5}, false},
		{"pool fills in three batches", []int{0, 5, 10, 15, 15, 15, 15, 15, 15, 15, 15}, false},
		{"pool fills in half the batches", []int{0, 2, 4, 6, 8, 10, 10, 10, 10, 10, 10}, false},
		{"one step at the end", []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 12}, false},
		{"oscillating", []int{0, 8, 0, 8, 0, 8, 0, 8, 0, 8, 0}, false},
		// pidfd counts seen in the remote execution soak, while the executor
		// waits for commands asynchronously.
		{"released in bulk", []int{1, 1, 1, 23, 1, 1, 23, 19, 23, 23, 23}, false},
		{"shrinking", []int{29, 29, 29, 29, 29, 29, 29, 1, 1, 23, 23}, false},
		// Limitations.
		{"less than one per batch is not caught", []int{0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5}, false},
		{"pool that grows past the middle is reported", []int{0, 2, 4, 6, 8, 10, 12, 14, 14, 14, 14}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, soakGrowing(tc.series), "series %v", tc.series)
		})
	}
}

const soakBatches = 6

func TestSoak_Leak_Caught(t *testing.T) {
	skipIfNoFDs(t)
	dogfood(t)
	stop := make(chan struct{})
	defer close(stop)
	var files []*os.File
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	p := &pool{size: 4, stop: make(chan struct{})}
	defer p.Close()

	f := newFakeTB(t)
	Soak(f, 1, soakBatches, func(int) {
		p.use()
		for range 3 {
			go leakyWorker(stop)
			file, err := os.Open(os.DevNull)
			require.NoError(t, err)
			files = append(files, file)
		}
	})

	errs := f.allErrors()
	t.Logf("report (expected):\n%s", errs)
	require.Len(t, f.errors, 2, "expected only the leaked goroutines and FDs")
	require.Contains(t, errs, "series=[3 6 9 12 15 18 21] group=goroutine stack:\n  github.com/buildbuddy-io/buildbuddy/server/testutil/testleak.leakyWorker")
	require.Contains(t, errs, "group=fds: file /dev\n  example: /dev/null")
}

func TestSoak_NoisyLeak_Caught(t *testing.T) {
	dogfood(t)
	var stops []chan struct{}
	defer func() {
		for _, stop := range stops {
			close(stop)
		}
	}()

	f := newFakeTB(t)
	Soak(f, 1, soakBatches, func(i int) {
		for range 2 {
			stop := make(chan struct{})
			stops = append(stops, stop)
			go leakyWorker(stop)
		}
		if i == 3 {
			// Some of the leaked goroutines happen to exit, so the count dips
			// once, but keeps growing by about two per batch.
			for _, stop := range stops[:3] {
				close(stop)
			}
			stops = stops[3:]
		}
	})

	msg := f.requireOneError(t)
	require.Contains(t, msg, "series=[2 4 6 5 7 9 11] group=goroutine stack:\n  github.com/buildbuddy-io/buildbuddy/server/testutil/testleak.leakyWorker")
}

func TestSoak_BoundedPoolAndCleanup_Accepted(t *testing.T) {
	dogfood(t)
	p := &pool{size: 3, stop: make(chan struct{})}
	defer p.Close()

	f := newFakeTB(t)
	Soak(f, 1, soakBatches, func(int) {
		p.use()
		stop := make(chan struct{})
		for range 5 {
			go leakyWorker(stop)
		}
		file, err := os.Open(os.DevNull)
		require.NoError(t, err)
		close(stop)
		require.NoError(t, file.Close())
	})
	f.requireNoErrors(t)
}
