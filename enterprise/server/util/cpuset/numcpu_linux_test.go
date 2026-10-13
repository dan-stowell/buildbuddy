//go:build linux && !android

package cpuset

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGetCPUsFromSysfs(t *testing.T) {
	for _, tc := range []struct {
		name            string
		missingTopology string
		files           map[string]string
		want            []CPUInfo
		wantError       bool
	}{
		{name: "ranges and commas", files: map[string]string{"node/node0/cpulist": "0-2,4,6-7\n"}, want: []CPUInfo{{0, 0}, {1, 0}, {2, 0}, {4, 0}, {6, 0}, {7, 0}}},
		{name: "multiple noncontiguous nodes", files: map[string]string{"node/node0/cpulist": "0,2\n", "node/node3/cpulist": "1,3\n", "node/node4/cpulist": "\n"}, want: []CPUInfo{{0, 0}, {2, 0}, {1, 3}, {3, 3}}},
		{name: "missing node directory", files: map[string]string{"cpu/cpu0/topology/physical_package_id": "2\n", "cpu/cpu1/topology/physical_package_id": "5\n", "cpu/cpu2/unrelated": ""}, want: []CPUInfo{{0, 2}, {1, 5}}},
		{name: "missing package IDs", files: map[string]string{"cpu/cpu0/unrelated": ""}, want: []CPUInfo{}},
		{name: "offline fallback CPU", files: map[string]string{"cpu/cpu0/topology/physical_package_id": "0", "cpu/cpu1/topology/physical_package_id": "0", "cpu/online": "0"}, want: []CPUInfo{{0, 0}}},
		{name: "absent online file", files: map[string]string{"cpu/cpu0/topology/physical_package_id": "0", "cpu/cpu1/topology/physical_package_id": "0"}, want: []CPUInfo{{0, 0}, {1, 0}}},
		{name: "offline NUMA CPU", files: map[string]string{"node/node0/cpulist": "0-1", "cpu/online": "0"}, want: []CPUInfo{{0, 0}}},
		{name: "missing NUMA core ID", files: map[string]string{"node/node0/cpulist": "0"}, missingTopology: "core_id", want: []CPUInfo{}},
		{name: "missing NUMA package ID", files: map[string]string{"node/node0/cpulist": "0"}, missingTopology: "physical_package_id", want: []CPUInfo{}},
		{name: "missing fallback core ID", files: map[string]string{"cpu/cpu0/topology/physical_package_id": "0"}, missingTopology: "core_id", want: []CPUInfo{}},
		{name: "malformed online list", files: map[string]string{"node/node0/cpulist": "0", "cpu/online": "bad"}, want: []CPUInfo{}},
		{name: "invalid core ID", files: map[string]string{"cpu/cpu0/topology/physical_package_id": "0", "cpu/cpu0/topology/core_id": "bad"}, wantError: true},
		{name: "no CPUs", wantError: true},
		{name: "missing cpulist", files: map[string]string{"node/node0/unrelated": ""}, wantError: true},
		{name: "invalid range", files: map[string]string{"node/node0/cpulist": "3-1\n"}, wantError: true},
		{name: "invalid CPU", files: map[string]string{"node/node0/cpulist": "x\n"}, wantError: true},
		{name: "invalid package ID", files: map[string]string{"cpu/cpu0/topology/physical_package_id": "x\n"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			// Supply normal topology for CPUs in node lists and fallback fixtures.
			defaults := map[string]string{}
			for name, data := range tc.files {
				if filepath.Base(name) == "cpulist" {
					infos, err := parseCPUs(strings.TrimSpace(data))
					if err == nil {
						for _, info := range infos {
							for _, field := range []string{"core_id", "physical_package_id", "book_id", "drawer_id"} {
								defaults[fmt.Sprintf("cpu/cpu%d/topology/%s", info.Processor, field)] = "0"
							}
						}
					}
				}
				if filepath.Base(name) == "physical_package_id" {
					for _, field := range []string{"core_id", "book_id", "drawer_id"} {
						defaults[filepath.Join(filepath.Dir(name), field)] = "0"
					}
				}
			}
			maps.Copy(defaults, tc.files)
			if tc.missingTopology != "" {
				delete(defaults, "cpu/cpu0/topology/"+tc.missingTopology)
			}
			for name, data := range defaults {
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := getCPUs(root)
			if (err != nil) != tc.wantError {
				t.Fatalf("getCPUs() error = %v, wantError %v", err, tc.wantError)
			}
			if !tc.wantError && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("getCPUs() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCPUInOnlineList(t *testing.T) {
	for _, tc := range []struct {
		list string
		cpu  uint16
		want bool
	}{
		{"0,2-4,7\n", 3, true},
		{"0,2-4,7", 1, false},
		{"", 0, false},
		{"4-2", 3, false},
		{"0,bad", 0, true},
		{"bad,0", 0, false},
		{"0-1-2", 0, false},
		{"65536", 0, false},
	} {
		if got := cpuInOnlineList(tc.list, tc.cpu); got != tc.want {
			t.Errorf("cpuInOnlineList(%q, %d) = %v, want %v", tc.list, tc.cpu, got, tc.want)
		}
	}
}
