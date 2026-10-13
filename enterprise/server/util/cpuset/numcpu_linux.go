//go:build linux && !android

package cpuset

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func GetCPUs() ([]CPUInfo, error) {
	return getCPUs("/sys/devices/system")
}

func getCPUs(root string) ([]CPUInfo, error) {
	nodes, err := filepath.Glob(filepath.Join(root, "node", "node*[0-9]"))
	if err != nil {
		return nil, err
	}
	cpus := make([]CPUInfo, 0)
	for _, node := range nodes {
		id, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(node), "node"))
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(filepath.Join(node, "cpulist"))
		if err != nil {
			return nil, err
		}
		list := strings.TrimSpace(string(data))
		if list == "" {
			continue // Memory-only NUMA node.
		}
		infos, err := parseCPUs(list)
		if err != nil {
			return nil, fmt.Errorf("parse %s/cpulist: %w", node, err)
		}
		for _, info := range infos {
			ok, err := cpuAvailable(root, filepath.Join(root, "cpu", fmt.Sprintf("cpu%d", info.Processor)), info.Processor)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			info.NumaNode = id
			cpus = append(cpus, info)
		}
	}
	if len(nodes) > 0 {
		return cpus, nil
	}

	// Without NUMA topology, cAdvisor grouped CPUs by physical package ID.
	paths, err := filepath.Glob(filepath.Join(root, "cpu", "cpu*[0-9]"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no CPU is available, cpusPath: %s", filepath.Join(root, "cpu"))
	}
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(path, "topology", "physical_package_id"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		id, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, err
		}
		processor, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(path), "cpu"))
		if err != nil {
			return nil, err
		}
		ok, err := cpuAvailable(root, path, processor)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		cpus = append(cpus, CPUInfo{Processor: processor, NumaNode: id})
	}
	return cpus, nil
}

// Read the online list for each CPU, as cAdvisor did, so hotplug changes are
// observed during discovery. Kernels without CPU hotplug omit this file.
func cpuAvailable(root, path string, processor int) (bool, error) {
	onlinePath := filepath.Join(root, "cpu", "online")
	if _, err := os.Stat(onlinePath); !os.IsNotExist(err) {
		data, err := os.ReadFile(onlinePath)
		if err != nil || !cpuInOnlineList(string(data), uint16(processor)) {
			return false, nil
		}
	}
	for _, name := range []string{"core_id", "physical_package_id"} {
		data, err := os.ReadFile(filepath.Join(path, "topology", name))
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if _, err := strconv.Atoi(strings.TrimSpace(string(data))); err != nil {
			return false, err
		}
	}
	if runtime.GOARCH == "s390x" {
		for _, name := range []string{"book_id", "drawer_id"} {
			if _, err := os.ReadFile(filepath.Join(path, "topology", name)); err != nil {
				if os.IsNotExist(err) {
					return false, nil
				}
				return false, err
			}
		}
	}
	return true, nil
}

// Match cAdvisor's online-list parsing, including stopping at the first match
// and treating unreadable or malformed entries as offline.
func cpuInOnlineList(list string, cpu uint16) bool {
	for entry := range strings.SplitSeq(strings.TrimSpace(list), ",") {
		bounds := strings.Split(entry, "-")
		if len(bounds) > 2 {
			return false
		}
		low, err := strconv.ParseUint(bounds[0], 10, 16)
		if err != nil {
			return false
		}
		high := low
		if len(bounds) == 2 {
			high, err = strconv.ParseUint(bounds[1], 10, 16)
			if err != nil || low > high {
				return false
			}
		}
		if uint64(cpu) >= low && uint64(cpu) <= high {
			return true
		}
	}
	return false
}
