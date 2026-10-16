package testleak

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

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
	require.Contains(t, errs, "SOAK GROWTH candidate: goroutine: github.com/buildbuddy-io/buildbuddy/server/testutil/testleak.leakyWorker")
	require.Contains(t, errs, "SOAK GROWTH candidate: fd: file /dev series=")
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
