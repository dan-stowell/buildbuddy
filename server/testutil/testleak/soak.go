package testleak

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	allGoroutinesGroup = "goroutines: all"
	allFDsGroup        = "fds: all"
)

// Soak runs op for warmup batches, then for the given number of measured
// batches, sampling the process's goroutines and file descriptors once things
// have settled after each measured batch (and once before the first). It fails
// the test for each resource group whose count grows steadily over the
// measured batches (see soakGrowing), and logs the count series of every group
// that changed. op is called with the batch number, starting from 0 (including
// warmup batches).
//
// Unlike CheckGrowth, Soak needs no bound or knowledge of what op uses: it is
// meant for long, mixed workloads against long-lived services (an app and an
// executor), to find accumulation in code nobody thought to target. Like
// CheckGrowth, the owners stay alive, so a flagged group is a candidate that
// needs ownership investigation, not proof of a leak.
//
// Groups are nested, so that a leak is caught even if its resources don't all
// look the same:
//
//   - goroutines with the same stack (function names, without arguments,
//     offsets or goroutine IDs) and creator function,
//   - within those, goroutines with the same creator and entry function,
//   - all goroutines;
//   - file descriptors of the same class (socket protocol, pipe, anon_inode
//     type, or the first two directories of a file's path, with digits
//     ignored),
//   - all file descriptors.
//
// A group is only reported if none of the groups nested in it is, so a leak is
// reported once, at the most specific level at which it is visible. The
// coarse groups also add up unrelated changes, so a leak can be hidden by an
// unrelated group shrinking at the same time; see soakGrowing for other
// limitations.
//
// Each sample is taken when three consecutive observations, 100ms apart, are
// identical, or after DefaultSettle; periodic background work can delay that.
// Do not run other tests in parallel.
func Soak(t testing.TB, warmup, batches int, op func(batch int)) {
	t.Helper()
	if warmup < 0 || batches < 4 {
		t.Fatal("testleak.Soak: needs a nonnegative warmup and at least four measured batches")
	}
	checkFDs := fdsSupported()
	if checkFDs {
		warmNetpoller()
	}
	parents := map[string]string{}
	examples := map[string]string{}
	snapshot := func() map[string]int {
		counts := map[string]int{}
		add := func(chain ...string) {
			for i, k := range chain {
				counts[k]++
				if i+1 < len(chain) {
					parents[k] = chain[i+1]
				}
			}
		}
		for _, g := range allGoroutines() {
			if isTestingGoroutine(g) {
				continue
			}
			k := g.replacementKey()
			kind := "goroutines: " + k.creator
			if k.entry != "" {
				kind += " running " + k.entry
			}
			add("goroutine stack:\n  "+strings.Join(g.functions, "\n  ")+"\n  "+k.creator, kind, allGoroutinesGroup)
		}
		if !checkFDs {
			return counts
		}
		fds, err := readFDs()
		if err != nil {
			t.Fatalf("testleak.Soak: %s", err)
		}
		var entries []fdEntry
		for fd, target := range fds {
			entries = append(entries, fdEntry{fd: fd, target: target, desc: target})
		}
		resolveFDs(entries)
		for _, e := range entries {
			k := "fds: " + soakFDClass(e)
			add(k, allFDsGroup)
			examples[k] = e.desc
		}
		return counts
	}
	settle := func() map[string]int {
		deadline := time.Now().Add(DefaultSettle)
		last := snapshot()
		for stable := 0; stable < 3; {
			if time.Now().After(deadline) {
				t.Logf("testleak.Soak: counts still changing after %s, sampling anyway", DefaultSettle)
				break
			}
			time.Sleep(100 * time.Millisecond)
			next := snapshot()
			if maps.Equal(next, last) {
				stable++
			} else {
				stable = 0
			}
			last = next
		}
		return last
	}

	for i := range warmup {
		op(i)
	}
	samples := []map[string]int{settle()}
	for i := range batches {
		op(warmup + i)
		samples = append(samples, settle())
	}

	series := map[string][]int{}
	for _, s := range samples {
		for k := range s {
			series[k] = nil
		}
	}
	for k := range series {
		for _, s := range samples {
			series[k] = append(series[k], s[k])
		}
	}
	// A group is covered if a group nested in it is flagged, or covered.
	covered := map[string]bool{}
	for k, s := range series {
		if soakGrowing(s) {
			for p, ok := parents[k]; ok; p, ok = parents[p] {
				covered[p] = true
			}
		}
	}
	delta := func(k string) int { s := series[k]; return s[len(s)-1] - s[0] }
	keys := slices.Collect(maps.Keys(series))
	slices.SortFunc(keys, func(a, b string) int {
		if d := delta(b) - delta(a); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	constant := 0
	for _, k := range keys {
		s := series[k]
		if slices.Min(s) == slices.Max(s) {
			constant++
			continue
		}
		t.Logf("testleak.Soak: delta=%+d series=%v group=%s", delta(k), s, k)
		if example, ok := examples[k]; ok {
			t.Logf("  example: %s", example)
		}
	}
	t.Logf("testleak.Soak: %d resource groups over %d measured batches, %d of them constant", len(keys), batches, constant)
	for _, k := range keys {
		if soakGrowing(series[k]) && !covered[k] {
			msg := "testleak.Soak: resources grew steadily over %d batches: series=%v group=%s"
			if example, ok := examples[k]; ok {
				msg += "\n  example: " + example
			}
			t.Errorf(msg, batches, series[k], k)
		}
	}
}

// soakFDClass groups file descriptors by what they point to.
func soakFDClass(e fdEntry) string {
	switch {
	case strings.HasPrefix(e.target, "socket:"):
		// desc is e.g. "socket:[123] tcp 127.0.0.1:1 -> 127.0.0.1:2 ESTABLISHED"
		for _, proto := range []string{"tcp6", "tcp", "udp6", "udp", "unix"} {
			if strings.Contains(e.desc, " "+proto+" ") {
				return "socket " + proto
			}
		}
		return "socket (unresolved)"
	case strings.HasPrefix(e.target, "pipe:"):
		return "pipe"
	case strings.HasPrefix(e.target, "/"):
		parts := strings.Split(strings.TrimPrefix(filepath.Dir(e.target), "/"), "/")
		return "file /" + digitsRegexp.ReplaceAllString(strings.Join(parts[:min(2, len(parts))], "/"), "#")
	}
	return e.target
}

// soakGrowing reports whether a series of counts, one per sample (the first
// taken after the warmup), shows sustained growth. That is the case when:
//
//   - the overall trend is at least one resource per batch (the trend is the
//     median of the slopes between all pairs of samples, so that noise and
//     occasional dips don't hide growth, and a few outliers don't create it),
//   - the trend over the second half of the samples is still at least half a
//     resource per batch, so that a pool that grows during the first half
//     and then stays flat is not reported, and
//   - the total of all decreases is at most half the net increase, so that a
//     group whose count repeatedly drops back (resources being released) is
//     not reported.
//
// Limitations: a leak of less than one resource per batch on average is not
// reported (make the batches bigger to catch rarer leaks); a pool that keeps
// growing past the middle of the measured batches is (use more warmup
// batches). Growth that stops early in the measured batches is not reported,
// whether it is a bounded pool or a leak that only happens for a while.
func soakGrowing(series []int) bool {
	n := len(series) - 1
	if n < 2 {
		return false
	}
	drops := 0
	for i := 1; i <= n; i++ {
		drops += max(series[i-1]-series[i], 0)
	}
	net := series[n] - series[0]
	return medianSlope(series) >= 1 && medianSlope(series[n/2:]) >= 0.5 && 2*drops <= net
}

// medianSlope returns the median of the slopes between all pairs of points
// (the Theil-Sen estimator), with one point per batch.
func medianSlope(series []int) float64 {
	var slopes []float64
	for i := range series {
		for j := i + 1; j < len(series); j++ {
			slopes = append(slopes, float64(series[j]-series[i])/float64(j-i))
		}
	}
	slices.Sort(slopes)
	m := len(slopes) / 2
	if len(slopes)%2 == 0 {
		return (slopes[m-1] + slopes[m]) / 2
	}
	return slopes[m]
}
