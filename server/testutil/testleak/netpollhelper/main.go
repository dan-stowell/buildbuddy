// netpollhelper is used by testleak's tests to check how CheckFDs handles the
// file descriptors that the Go runtime opens when the network poller is first
// used. It needs to be a separate binary because Go test binaries built by
// Bazel initialize the poller before any test runs: on Linux, opening any
// regular file with the os package initializes it.
//
// Usage: netpollhelper warm|nowarm
//
//   - nowarm: lists the FDs that appear after the first network use.
//   - warm: uses testleak.CheckFDs around the first network use, and prints
//     whether it reported a leak.
package main

import (
	"fmt"
	"maps"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testleak"
)

// listFDs lists the open FDs without opening a regular file (opening a
// directory doesn't initialize the poller).
func listFDs() map[string]string {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		fail(err)
	}
	fds := map[string]string{}
	for _, e := range entries {
		if target, err := os.Readlink("/proc/self/fd/" + e.Name()); err == nil {
			fds[e.Name()] = target
		}
	}
	return fds
}

func useNetwork() {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail(err)
	}
	conn, err := net.Dial("tcp", lis.Addr().String())
	if err != nil {
		fail(err)
	}
	conn.Close()
	lis.Close()
}

type fakeTB struct {
	testing.TB
	errors   []string
	cleanups []func()
}

func (f *fakeTB) Helper()                         {}
func (f *fakeTB) Name() string                    { return "netpollhelper" }
func (f *fakeTB) Cleanup(fn func())               { f.cleanups = append(f.cleanups, fn) }
func (f *fakeTB) Logf(format string, args ...any) { fmt.Printf("log: "+format+"\n", args...) }
func (f *fakeTB) Errorf(format string, args ...any) {
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}

func fail(err error) {
	fmt.Printf("HELPER: error: %s\n", err)
	os.Exit(1)
}

func main() {
	if len(os.Args) != 2 {
		fail(fmt.Errorf("usage: %s warm|nowarm", os.Args[0]))
	}
	before := listFDs()
	initialized := slices.Contains(slices.Collect(maps.Values(before)), "anon_inode:[eventpoll]")
	fmt.Printf("HELPER: netpoller initialized at start: %v\n", initialized)

	switch os.Args[1] {
	case "nowarm":
		useNetwork()
		var added []string
		for fd, target := range listFDs() {
			if before[fd] != target {
				added = append(added, fd+" -> "+target)
			}
		}
		slices.Sort(added)
		fmt.Printf("HELPER: new fds after first network use: %s\n", strings.Join(added, ", "))
	case "warm":
		f := &fakeTB{}
		testleak.CheckFDs(f, testleak.FDSettle(time.Second))
		useNetwork()
		for _, fn := range slices.Backward(f.cleanups) {
			fn()
		}
		if len(f.errors) > 0 {
			fmt.Printf("HELPER: leak reported:\n%s\n", strings.Join(f.errors, "\n"))
		} else {
			fmt.Printf("HELPER: no leak reported\n")
		}
	default:
		fail(fmt.Errorf("unknown mode %q", os.Args[1]))
	}
}
