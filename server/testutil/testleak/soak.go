package testleak

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Soak runs warmup batches followed by measured batches. It reports all resource
// groups, ranked by net growth, and fails on monotone growth of at least one
// resource per measured batch, with increases in at least two thirds of batches.
// A single pool expansion followed by a plateau is only reported in the series.
// Owners may remain running; flags are candidates
// requiring ownership investigation, not proof of leaks. Do not run in parallel.
// Each sample polls for a stable count vector (three equal observations), with
// a five second deadline. Periodic background work can prevent convergence.
func Soak(t testing.TB, warmup, batches int, op func(int)) {
	t.Helper()
	if warmup < 0 || batches < 3 {
		t.Fatal("soak needs nonnegative warmup and at least three measured batches")
	}
	warmNetpoller()
	examples := map[string]string{}
	snapshot := func() map[string]int {
		m := map[string]int{}
		for _, g := range allGoroutines() {
			if isTestingGoroutine(g) {
				continue
			}
			creator, _, _ := strings.Cut(g.creator, " in goroutine ")
			m["goroutine: "+strings.Join(g.functions, "\n  ")+"\n  "+creator]++
		}
		fds, err := readFDs()
		if err != nil {
			t.Fatalf("soak snapshot: %s", err)
		}
		entries := []fdEntry{}
		for fd, target := range fds {
			entries = append(entries, fdEntry{fd: fd, target: target, desc: target})
		}
		resolveFDs(entries)
		for _, e := range entries {
			k := e.target
			switch {
			case strings.HasPrefix(k, "socket:"):
				k = "socket unknown"
				for _, proto := range []string{"tcp6", "tcp", "udp6", "udp", "unix"} {
					if strings.Contains(e.desc, " "+proto+" ") {
						k = "socket " + proto
						break
					}
				}
			case strings.HasPrefix(k, "pipe:"):
				k = "pipe"
			case strings.HasPrefix(k, "/"):
				parts := strings.Split(strings.TrimPrefix(filepath.Dir(k), "/"), "/")
				k = "file /" + strings.Join(parts[:min(2, len(parts))], "/")
				k = digitsRegexp.ReplaceAllString(k, "#")
			}
			m["fd: "+k]++
			examples["fd: "+k] = e.desc
		}
		return m
	}
	settle := func() map[string]int {
		deadline := time.Now().Add(DefaultSettle)
		last := snapshot()
		stable := 0
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for time.Now().Before(deadline) {
			<-ticker.C
			next := snapshot()
			if maps.Equal(next, last) {
				stable++
			} else {
				stable = 0
			}
			last = next
			if stable >= 3 {
				return last
			}
		}
		t.Log("soak sample reached settle deadline")
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
	keys := map[string]bool{}
	for _, s := range samples {
		for k := range s {
			keys[k] = true
		}
	}
	ranked := []string{}
	for k := range keys {
		ranked = append(ranked, k)
	}
	delta := func(k string) int { return samples[batches][k] - samples[0][k] }
	slices.SortFunc(ranked, func(a, b string) int {
		if d := delta(b) - delta(a); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	for _, k := range ranked {
		series := []int{}
		monotone := true
		growing := 0
		for i, s := range samples {
			series = append(series, s[k])
			if i > 0 && s[k] > samples[i-1][k] {
				growing++
			}
			if i > 0 && s[k] < samples[i-1][k] {
				monotone = false
			}
		}
		t.Logf("soak delta=%+d series=%v group=%s", delta(k), series, k)
		if example, ok := examples[k]; ok {
			t.Logf("  example FD target: %s", example)
		}
		if monotone && growing >= (2*batches+2)/3 && delta(k) >= batches {
			t.Errorf("SOAK GROWTH candidate: %s series=%v", k, series)
		}
	}
}
