package testleak

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// Growth configures CheckGrowth.
type Growth struct {
	// Iterations is how many times to run the operation after the warmup.
	// Defaults to 20.
	Iterations int

	// Warmup is how many times to run the operation before taking the
	// baseline, so that lazily created long-lived resources (pools, caches,
	// background workers, the network poller) exist before counting starts.
	// Defaults to 1. Set to a negative value to disable.
	Warmup int

	// MaxGoroutines is the number of goroutines that may remain after all
	// Iterations and the settle period, compared to the baseline. Use this to
	// allow for bounded pools that grow on demand. To catch a leak of one
	// resource per operation, keep it below Iterations.
	MaxGoroutines int

	// MaxFDs is like MaxGoroutines, for file descriptors (Linux only).
	MaxFDs int

	// SkipFDs disables counting file descriptors.
	SkipFDs bool

	// IgnoreFunctions lists fully qualified function names (as they appear in
	// stack traces, e.g. "net/http.(*persistConn).readLoop"); goroutines with
	// any of these functions anywhere in their stack are not counted.
	IgnoreFunctions []string

	// Settle is how long to wait for resources to be released after the last
	// iteration before reporting growth. Defaults to DefaultSettle.
	Settle time.Duration
}

// CheckGrowth runs op Warmup+Iterations times, and then fails the test if the
// number of goroutines or file descriptors that were created after the
// warmup and are still around once things have settled exceeds MaxGoroutines
// or MaxFDs. op is called with the iteration number, starting from 0
// (including warmup iterations).
//
// Unlike Check and CheckFDs, it runs synchronously and doesn't care what
// happens at the end of the test, so it can be used to find leaks that
// accumulate in long-lived objects that are only closed at the end of the
// test, e.g. a connection pool that never evicts dead connections.
//
// The failure message reports the growth per iteration (measured right after
// each op, without settling) and the remaining goroutines and FDs. Linear
// growth over the iterations points to a leak per operation; a step at the
// start that then stays flat points to a bounded pool, which should be
// covered by Warmup or MaxGoroutines/MaxFDs.
func CheckGrowth(t testing.TB, g Growth, op func(i int)) {
	t.Helper()
	if g.Iterations <= 0 {
		g.Iterations = 20
	}
	if g.Warmup == 0 {
		g.Warmup = 1
	}
	if g.Settle <= 0 {
		g.Settle = DefaultSettle
	}
	checkFDs := !g.SkipFDs && fdsSupported()

	i := 0
	if checkFDs {
		warmNetpoller()
	}
	for ; i < g.Warmup; i++ {
		op(i)
	}

	baseGoroutines := map[int]bool{}
	for _, gr := range allGoroutines() {
		baseGoroutines[gr.id] = true
	}
	var baseFDs map[int]string
	if checkFDs {
		var err error
		if baseFDs, err = readFDs(); err != nil {
			t.Logf("testleak: not checking file descriptors: %s", err)
			checkFDs = false
		}
	}

	measure := func() ([]goroutine, []fdEntry) {
		var gs []goroutine
		for _, gr := range allGoroutines() {
			if baseGoroutines[gr.id] || isTestingGoroutine(gr) {
				continue
			}
			if slices.ContainsFunc(g.IgnoreFunctions, func(f string) bool { return slices.Contains(gr.functions, f) }) {
				continue
			}
			gs = append(gs, gr)
		}
		var fds []fdEntry
		if checkFDs {
			if after, err := readFDs(); err == nil {
				fds = newFDs(baseFDs, after)
			}
		}
		return gs, fds
	}

	type sample struct{ goroutines, fds int }
	var samples []sample
	for n := 0; n < g.Iterations; n, i = n+1, i+1 {
		op(i)
		gs, fds := measure()
		samples = append(samples, sample{len(gs), len(fds)})
	}

	start := time.Now()
	var gs []goroutine
	var fds []fdEntry
	for {
		gs, fds = measure()
		if len(gs) <= g.MaxGoroutines && len(fds) <= g.MaxFDs {
			return
		}
		if time.Since(start) > g.Settle {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "resources grew over %d iterations of the operation (after %d warmup iteration(s) and %s settling):\n",
		g.Iterations, max(g.Warmup, 0), time.Since(start).Round(time.Millisecond))
	fmt.Fprintf(&b, "  goroutines: +%d (%.2f per iteration, max allowed %d)\n",
		len(gs), float64(len(gs))/float64(g.Iterations), g.MaxGoroutines)
	if checkFDs {
		fmt.Fprintf(&b, "  file descriptors: +%d (%.2f per iteration, max allowed %d)\n",
			len(fds), float64(len(fds))/float64(g.Iterations), g.MaxFDs)
	}
	b.WriteString("  growth after each iteration, before settling (iteration: goroutines")
	if checkFDs {
		b.WriteString(", fds")
	}
	b.WriteString("):")
	for n, s := range samples {
		if n%10 == 0 {
			b.WriteString("\n   ")
		}
		if checkFDs {
			fmt.Fprintf(&b, " %d:+%d,+%d", n+1, s.goroutines, s.fds)
		} else {
			fmt.Fprintf(&b, " %d:+%d", n+1, s.goroutines)
		}
	}
	b.WriteString("\n")
	if len(gs) > g.MaxGoroutines {
		b.WriteString("\nRemaining new goroutines: ")
		b.WriteString(formatGoroutines(gs, 0))
	}
	if len(fds) > g.MaxFDs {
		resolveFDs(fds)
		b.WriteString("\nRemaining new ")
		b.WriteString(describeFDs(fds))
	}
	t.Errorf("%s: %s", t.Name(), b.String())
}
