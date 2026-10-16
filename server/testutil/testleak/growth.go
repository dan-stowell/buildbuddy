package testleak

import (
	"fmt"
	"maps"
	"regexp"
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

	// AllowGC lets the garbage collector run normally while counting file
	// descriptors; see the AllowGC FDOption. By default, the GC is held from
	// the baseline (after the warmup) until the end of the settle period,
	// so FDs that are only closed by finalizers are counted.
	AllowGC bool

	// Settle is how long to wait for resources to be released after the last
	// iteration before reporting growth. Defaults to DefaultSettle.
	Settle time.Duration
}

// CheckGrowth runs op Warmup+Iterations times, and then fails the test if the
// net increase in live goroutines or file descriptors after the
// warmup, once things have settled, exceeds MaxGoroutines
// or MaxFDs. op is called with the iteration number, starting from 0
// (including warmup iterations).
//
// Unlike Check and CheckFDs, it runs synchronously and doesn't care what
// happens at the end of the test, so it can be used to find leaks that
// accumulate in long-lived objects that are only closed at the end of the
// test, e.g. a connection pool that never evicts dead connections.
//
// A baseline resource that is gone (retired) offsets one new resource of the
// same kind, so that an owner that replaces a resource on each operation
// (closing the old one first) is not reported. Goroutines are the same kind if
// they run the same function and were started by the same function (just the
// same starter if either had not started running when sampled); FDs
// if their targets match with all digits ignored (e.g. any socket, any pipe,
// or files whose paths differ only in numbers). Retired resources of a
// different kind, e.g. an unrelated setup goroutine exiting, do not offset
// anything, so they cannot mask a leak.
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

	if checkFDs && !g.AllowGC {
		hold := holdGC()
		defer func() {
			if note := gcNote(hold.release()); note != "" {
				t.Logf("%s", note)
			}
		}()
	}

	// Baseline goroutines and FDs, with the key used to match them to
	// replacements.
	baseGoroutines := map[int]replacementKey{}
	eligible := func(gr goroutine) bool {
		return !isTestingGoroutine(gr) && !slices.ContainsFunc(g.IgnoreFunctions, func(f string) bool { return slices.Contains(gr.functions, f) })
	}
	for _, gr := range allGoroutines() {
		if eligible(gr) {
			baseGoroutines[gr.id] = gr.replacementKey()
		}
	}
	var baseFDs map[int]string
	if checkFDs {
		var err error
		if baseFDs, err = readFDs(); err != nil {
			t.Logf("testleak: not checking file descriptors: %s", err)
			checkFDs = false
		}
	}

	// measure returns the new goroutines and FDs that are not offset by a
	// retired baseline resource of the same kind, and how many new ones
	// there are in total.
	measure := func() (gs []goroutine, fds []fdEntry, newGoroutines, newFDCount int) {
		var fresh []goroutine
		retired := maps.Clone(baseGoroutines)
		for _, gr := range allGoroutines() {
			if !eligible(gr) {
				continue
			}
			if _, ok := baseGoroutines[gr.id]; ok {
				delete(retired, gr.id)
			} else {
				fresh = append(fresh, gr)
			}
		}
		gs = withoutGoroutineReplacements(fresh, slices.Collect(maps.Values(retired)))
		if checkFDs {
			after, err := readFDs()
			if err != nil {
				t.Errorf("testleak: read file descriptors: %s", err)
				return gs, nil, len(fresh), 0
			}
			var retiredFDs []string
			for fd, target := range baseFDs {
				if after[fd] != target {
					retiredFDs = append(retiredFDs, fdReplacementKey(target))
				}
			}
			freshFDs := newFDs(baseFDs, after)
			fds = withoutReplacements(freshFDs, retiredFDs, func(e fdEntry) string { return fdReplacementKey(e.target) })
			newFDCount = len(freshFDs)
		}
		return gs, fds, len(fresh), newFDCount
	}

	type sample struct{ goroutines, fds int }
	var samples []sample
	for n := 0; n < g.Iterations; n, i = n+1, i+1 {
		op(i)
		gs, fds, _, _ := measure()
		samples = append(samples, sample{len(gs), len(fds)})
	}

	start := time.Now()
	var gs []goroutine
	var fds []fdEntry
	var newGoroutines, newFDCount int
	for {
		gs, fds, newGoroutines, newFDCount = measure()
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
	fmt.Fprintf(&b, "  goroutines: +%d (%.2f per iteration, max allowed %d; %d new, %d offset by retired replacements)\n",
		len(gs), float64(len(gs))/float64(g.Iterations), g.MaxGoroutines, newGoroutines, newGoroutines-len(gs))
	if checkFDs {
		fmt.Fprintf(&b, "  file descriptors: +%d (%.2f per iteration, max allowed %d; %d new, %d offset by retired replacements)\n",
			len(fds), float64(len(fds))/float64(g.Iterations), g.MaxFDs, newFDCount, newFDCount-len(fds))
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
		closedByGC := fdsClosedByGC(fds)
		b.WriteString("\nRemaining new ")
		b.WriteString(describeFDs(fds, closedByGC))
		b.WriteString(gcClosedNote(closedByGC))
	}
	t.Errorf("%s: %s", t.Name(), b.String())
}

// withoutReplacements removes from fresh one element for each retired key
// with the same key.
func withoutReplacements[T any](fresh []T, retired []string, key func(T) string) []T {
	credit := map[string]int{}
	for _, k := range retired {
		credit[k]++
	}
	var out []T
	for _, x := range fresh {
		if k := key(x); credit[k] > 0 {
			credit[k]--
			continue
		}
		out = append(out, x)
	}
	return out
}

// replacementKey identifies the kind of goroutine, for matching a new
// goroutine to the one it replaced: the function that started it, and the
// function it runs, which don't depend on what it is currently doing.
type replacementKey struct {
	creator string
	// entry is empty if unknown: the goroutine hasn't started yet (its stack
	// shows a compiler-generated go statement wrapper) or is running on
	// another thread (no stack available).
	entry string
}

var goWrapperRegexp = regexp.MustCompile(`\.gowrap\d+$`)

func (g goroutine) replacementKey() replacementKey {
	k := replacementKey{}
	k.creator, _, _ = strings.Cut(g.creator, " in goroutine ")
	if fns := slices.DeleteFunc(slices.Clone(g.functions), func(f string) bool { return f == "runtime.goexit" }); len(fns) > 0 {
		k.entry = fns[len(fns)-1]
	}
	if goWrapperRegexp.MatchString(k.entry) {
		k.entry = ""
	}
	return k
}

// withoutGoroutineReplacements removes from fresh one goroutine for each
// retired goroutine of the same kind. If either entry function is unknown,
// the same creator is enough.
func withoutGoroutineReplacements(fresh []goroutine, retired []replacementKey) []goroutine {
	keys := make([]replacementKey, len(fresh))
	for i, g := range fresh {
		keys[i] = g.replacementKey()
	}
	matched := make([]bool, len(fresh))
	used := make([]bool, len(retired))
	match := func(loose bool) {
		for i, k := range keys {
			for j, r := range retired {
				if matched[i] || used[j] || k.creator != r.creator {
					continue
				}
				if k.entry == r.entry || (loose && (k.entry == "" || r.entry == "")) {
					matched[i], used[j] = true, true
				}
			}
		}
	}
	match(false)
	match(true)
	var out []goroutine
	for i, g := range fresh {
		if !matched[i] {
			out = append(out, g)
		}
	}
	return out
}

var digitsRegexp = regexp.MustCompile(`[0-9]+`)

// fdReplacementKey identifies the kind of file descriptor, for matching a new
// FD to the one it replaced: the target with numbers removed.
func fdReplacementKey(target string) string {
	return digitsRegexp.ReplaceAllString(target, "#")
}
