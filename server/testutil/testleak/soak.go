package testleak

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Soak runs warmup batches followed by measured batches. It reports all resource
// groups, ranked by net growth, and fails on monotone growth of at least one
// resource per measured batch. Owners may remain running; flags are candidates
// requiring ownership investigation, not proof of leaks. Do not run in parallel.
// Each sample polls for a stable count vector (three equal observations), with
// a five second deadline. Periodic background work can prevent convergence.
func Soak(t testing.TB, warmup, batches int, op func(int)) {
	t.Helper()
	warmNetpoller()
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
			entries = append(entries, fdEntry{fd: fd, target: target})
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
				k = "file " + digitsRegexp.ReplaceAllString(filepath.Dir(k), "#")
			}
			m["fd: "+k]++
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
			if fmt.Sprint(next) == fmt.Sprint(last) {
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
	for i := 0; i < warmup; i++ {
		op(i)
	}
	samples := []map[string]int{settle()}
	for i := 0; i < batches; i++ {
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
		for i, s := range samples {
			series = append(series, s[k])
			if i > 0 && s[k] < samples[i-1][k] {
				monotone = false
			}
		}
		t.Logf("soak delta=%+d series=%v group=%s", delta(k), series, k)
		if monotone && delta(k) >= batches {
			t.Errorf("SOAK GROWTH candidate: %s series=%v", k, series)
		}
	}
}
