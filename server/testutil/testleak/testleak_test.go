package testleak

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// ---------------------------------------------------------------------------
// Goroutines.
// ---------------------------------------------------------------------------

func TestGoroutine_Leak_Caught(t *testing.T) {
	dogfood(t)
	stop := make(chan struct{})
	defer close(stop)

	f := newFakeTB(t)
	checkGoroutines(f, failingSettle)
	go leakyWorker(stop)
	f.runCleanups()

	msg := f.requireOneError(t)
	require.Contains(t, msg, "leaked goroutines")
	require.Contains(t, msg, "1 x [chan receive] top: github.com/buildbuddy-io/buildbuddy/server/testutil/testleak.leakyWorker")
	require.Contains(t, msg, "created by github.com/buildbuddy-io/buildbuddy/server/testutil/testleak.TestGoroutine_Leak_Caught in goroutine")
	require.Contains(t, msg, "(the goroutine that called the check)")
	// Full stack with file and line.
	require.Contains(t, msg, "testleak_test.go:")
}

func TestGoroutine_ManyIdenticalLeaks_Grouped(t *testing.T) {
	dogfood(t)
	stop := make(chan struct{})
	defer close(stop)

	f := newFakeTB(t)
	checkGoroutines(f, failingSettle)
	for range 50 {
		go leakyWorker(stop)
	}
	go delayedWorker(stop, 0)
	f.runCleanups()

	msg := f.requireOneError(t)
	require.Contains(t, msg, "51 goroutine(s) in 2 distinct stack(s)")
	require.Contains(t, msg, "=== 50 x [chan receive] top: github.com/buildbuddy-io/buildbuddy/server/testutil/testleak.leakyWorker")
	require.Contains(t, msg, "=== 1 x [chan receive] top: github.com/buildbuddy-io/buildbuddy/server/testutil/testleak.delayedWorker")
}

func TestGoroutine_StoppedBeforeCleanup_Accepted(t *testing.T) {
	dogfood(t)
	f := newFakeTB(t)
	Check(f)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		leakyWorker(stop)
	}()
	close(stop)
	<-done
	f.runCleanups()
	f.requireNoErrors(t)
}

func TestGoroutine_DelayedExit_Accepted(t *testing.T) {
	for _, delay := range []time.Duration{200 * time.Millisecond, time.Second, 3 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			dogfood(t)
			f := newFakeTB(t)
			Check(f)
			stop := make(chan struct{})
			go delayedWorker(stop, delay)
			// A cleanup registered after Check runs before it.
			f.Cleanup(func() { close(stop) })
			start := time.Now()
			f.runCleanups()
			f.requireNoErrors(t)
			require.GreaterOrEqual(t, time.Since(start), delay, "the check should have waited for the goroutine to exit")
		})
	}
}

// goleak.Find on its own retries for only ~430ms, so it reports goroutines
// that exit 1s after their owner is closed. Check doesn't.
func TestAudit_GoleakFindSettleWindow(t *testing.T) {
	dogfood(t)
	opts := goleak.IgnoreCurrent()
	stop := make(chan struct{})
	go delayedWorker(stop, time.Second)
	close(stop)
	start := time.Now()
	err := goleak.Find(opts)
	elapsed := time.Since(start)
	t.Logf("goleak.Find gave up after %s with: %v", elapsed, err)
	require.Error(t, err, "expected goleak.Find to give up before the goroutine exits")
	require.Greater(t, elapsed, 300*time.Millisecond)
	require.Less(t, elapsed, 900*time.Millisecond)
	// Wait for the goroutine to exit.
	require.Eventually(t, func() bool { return goleak.Find(opts) == nil }, 5*time.Second, 50*time.Millisecond)
}

func TestGoroutine_StartedBeforeCheck_NotReported(t *testing.T) {
	dogfood(t)
	stop := make(chan struct{})
	// A long-lived background worker, e.g. a pool owned by the test's
	// environment, started before the check.
	go leakyWorker(stop)
	f := newFakeTB(t)
	Check(f)
	f.runCleanups()
	f.requireNoErrors(t)
	close(stop)
	require.Eventually(t, func() bool { return goleak.Find(goleak.IgnoreCurrent()) == nil }, 5*time.Second, 10*time.Millisecond)
}

func TestGoroutine_IgnoredByOption_NotReported(t *testing.T) {
	dogfood(t)
	stop := make(chan struct{})
	defer close(stop)
	f := newFakeTB(t)
	checkGoroutines(f, failingSettle, goleak.IgnoreAnyFunction("github.com/buildbuddy-io/buildbuddy/server/testutil/testleak.leakyWorker"))
	go leakyWorker(stop)
	go delayedWorker(stop, 0)
	f.runCleanups()
	// Only the delayed worker, which wasn't ignored, is reported.
	msg := f.allErrors()
	require.Contains(t, msg, "delayedWorker")
	require.NotContains(t, msg, "leakyWorker")
}

// owner is a long-lived object that starts goroutines while in use and stops
// them on Close, e.g. a server that starts a goroutine per connection.
type owner struct {
	stop chan struct{}
	wg   sync.WaitGroup
}

func newOwner() *owner { return &owner{stop: make(chan struct{})} }
func (o *owner) handle() {
	o.wg.Go(func() { leakyWorker(o.stop) })
}
func (o *owner) Close() {
	close(o.stop)
	o.wg.Wait()
}

// If Check is called after setting up something that registers a cleanup, its
// cleanup runs first, while that thing is still running.
func TestAudit_CleanupOrdering(t *testing.T) {
	t.Run("check before setup", func(t *testing.T) {
		dogfood(t)
		f := newFakeTB(t)
		Check(f)
		o := newOwner()
		f.Cleanup(o.Close)
		o.handle()
		f.runCleanups()
		f.requireNoErrors(t)
	})
	t.Run("check after setup is a false positive", func(t *testing.T) {
		dogfood(t)
		f := newFakeTB(t)
		o := newOwner()
		f.Cleanup(o.Close)
		checkGoroutines(f, failingSettle)
		o.handle()
		f.runCleanups()
		require.Contains(t, f.requireOneError(t), "leakyWorker")
	})
}

// Goroutines started by tests running in parallel are attributed to whichever
// test is checking.
func TestAudit_ParallelTestsCauseFalsePositives(t *testing.T) {
	var f *fakeTB
	checking := make(chan struct{})
	workerStarted := make(chan struct{})
	checked := make(chan struct{})
	t.Run("group", func(t *testing.T) {
		t.Run("checks", func(t *testing.T) {
			t.Parallel()
			f = newFakeTB(t)
			checkGoroutines(f, failingSettle)
			close(checking)
			<-workerStarted
			f.runCleanups()
			close(checked)
		})
		t.Run("does not leak", func(t *testing.T) {
			t.Parallel()
			<-checking
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				leakyWorker(stop)
			}()
			close(workerStarted)
			<-checked
			close(stop)
			<-done
		})
	})
	require.Contains(t, f.requireOneError(t), "leakyWorker")
}

// A goroutine that already exists when Check is called is never reported,
// even if the code under test was supposed to stop it.
func TestAudit_PreexistingGoroutineIsNotChecked(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	go leakyWorker(stop) // Supposed to be stopped by the test, but isn't.
	f := newFakeTB(t)
	Check(f)
	f.runCleanups()
	f.requireNoErrors(t)
}

// goleak ignores goroutines in state "syscall" if runtime.goexit is in their
// stack, which it is only with GOTRACEBACK=system or higher. Check that a
// goroutine blocked in a raw syscall is caught with the default traceback.
func TestAudit_GoroutineBlockedInSyscall_Caught(t *testing.T) {
	var fds [2]int
	require.NoError(t, syscall.Pipe(fds[:]))
	defer syscall.Close(fds[0])
	defer syscall.Close(fds[1])

	f := newFakeTB(t)
	checkGoroutines(f, failingSettle)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1)
		syscall.Read(fds[0], buf)
	}()
	f.runCleanups()
	syscall.Write(fds[1], []byte{0})
	<-done
	require.Contains(t, f.requireOneError(t), "[syscall]")
}

// ---------------------------------------------------------------------------
// File descriptors.
// ---------------------------------------------------------------------------

func TestFD_FileLeak_Caught(t *testing.T) {
	skipIfNoFDs(t)
	dogfood(t)
	path := filepath.Join(t.TempDir(), "leaked.txt")

	f := newFakeTB(t)
	CheckFDs(f, FDSettle(failingSettle))
	file, err := os.Create(path)
	require.NoError(t, err)
	defer file.Close()
	f.runCleanups()

	msg := f.requireOneError(t)
	require.Contains(t, msg, "leaked file descriptors")
	require.Contains(t, msg, "1 file descriptor(s): 1 file")
	require.Contains(t, msg, fmt.Sprintf("fd %d -> %s", file.Fd(), path))
}

func TestFD_SocketLeak_ResolvesAddresses(t *testing.T) {
	skipIfNoFDs(t)
	dogfood(t)

	f := newFakeTB(t)
	CheckFDs(f, FDSettle(failingSettle))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer lis.Close()
	client, err := net.Dial("tcp", lis.Addr().String())
	require.NoError(t, err)
	defer client.Close()
	server, err := lis.Accept()
	require.NoError(t, err)
	defer server.Close()
	f.runCleanups()

	msg := f.requireOneError(t)
	require.Contains(t, msg, "3 file descriptor(s): 3 socket")
	require.Contains(t, msg, fmt.Sprintf("tcp 127.0.0.1:%d -> 0.0.0.0:0 LISTEN", lis.Addr().(*net.TCPAddr).Port))
	require.Contains(t, msg, fmt.Sprintf("tcp %s -> %s ESTABLISHED", client.LocalAddr(), client.RemoteAddr()))
	require.Contains(t, msg, fmt.Sprintf("tcp %s -> %s ESTABLISHED", server.LocalAddr(), server.RemoteAddr()))
}

func TestFD_UnixSocketAndPipeLeak_Described(t *testing.T) {
	skipIfNoFDs(t)
	dogfood(t)
	// An abstract socket, since Bazel's temp dirs can be too long for a
	// socket path.
	sock := fmt.Sprintf("@testleak-%d-%d", os.Getpid(), time.Now().UnixNano())

	f := newFakeTB(t)
	CheckFDs(f, FDSettle(failingSettle))
	lis, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer lis.Close()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	defer w.Close()
	f.runCleanups()

	msg := f.requireOneError(t)
	require.Contains(t, msg, "unix "+sock)
	require.Contains(t, msg, fmt.Sprintf("(also open as fd %d)", w.Fd()))
	require.Contains(t, msg, "2 pipe")
}

func TestFD_Closed_Accepted(t *testing.T) {
	skipIfNoFDs(t)
	dogfood(t)
	f := newFakeTB(t)
	CheckFDs(f)
	file, err := os.Create(filepath.Join(t.TempDir(), "f"))
	require.NoError(t, err)
	require.NoError(t, file.Close())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, lis.Close())
	f.runCleanups()
	f.requireNoErrors(t)
}

func TestFD_DelayedClose_Accepted(t *testing.T) {
	skipIfNoFDs(t)
	for _, delay := range []time.Duration{200 * time.Millisecond, time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			dogfood(t)
			f := newFakeTB(t)
			CheckFDs(f)
			file, err := os.Create(filepath.Join(t.TempDir(), "f"))
			require.NoError(t, err)
			f.Cleanup(func() {
				time.AfterFunc(delay, func() { file.Close() })
			})
			start := time.Now()
			f.runCleanups()
			f.requireNoErrors(t)
			require.GreaterOrEqual(t, time.Since(start), delay)
		})
	}
}

func TestFD_OpenedBeforeCheck_NotReported(t *testing.T) {
	skipIfNoFDs(t)
	dogfood(t)
	// E.g. a database file owned by a long-lived test environment.
	file, err := os.Create(filepath.Join(t.TempDir(), "f"))
	require.NoError(t, err)
	defer file.Close()
	f := newFakeTB(t)
	CheckFDs(f)
	f.runCleanups()
	f.requireNoErrors(t)
}

func TestFD_IgnoredByOption_NotReported(t *testing.T) {
	skipIfNoFDs(t)
	dogfood(t)
	dir := t.TempDir()
	f := newFakeTB(t)
	CheckFDs(f, IgnoreFDs(`/singleton\.lock$`), FDSettle(failingSettle))
	ignored, err := os.Create(filepath.Join(dir, "singleton.lock"))
	require.NoError(t, err)
	defer ignored.Close()
	leaked, err := os.Create(filepath.Join(dir, "other"))
	require.NoError(t, err)
	defer leaked.Close()
	f.runCleanups()
	msg := f.requireOneError(t)
	require.Contains(t, msg, "1 file descriptor(s)")
	require.Contains(t, msg, "/other")
	require.NotContains(t, msg, "singleton.lock")
}

// Set by x_defs in BUILD.
var netpollHelperRlocationpath string

// The Go runtime opens an epoll fd and an eventfd the first time the network
// poller is used. In a fresh process, check that this happens, and that
// CheckFDs doesn't report them.
func TestFD_RuntimeNetpollerInFreshProcess(t *testing.T) {
	skipIfNoFDs(t)
	if netpollHelperRlocationpath == "" {
		t.Skip("only supported when run with Bazel")
	}
	helper, err := runfiles.Rlocation(netpollHelperRlocationpath)
	require.NoError(t, err)
	run := func(mode string) string {
		// If stdin, stdout or stderr is non-blocking, the os package
		// registers it with the poller during init. Use a regular file to
		// be safe.
		outFile, err := os.Create(filepath.Join(t.TempDir(), "out"))
		require.NoError(t, err)
		defer outFile.Close()
		cmd := exec.Command(helper, mode)
		cmd.Stdout = outFile
		cmd.Stderr = outFile
		runErr := cmd.Run()
		out, err := os.ReadFile(outFile.Name())
		require.NoError(t, err)
		t.Logf("helper %s output:\n%s", mode, out)
		require.NoError(t, runErr)
		require.Contains(t, string(out), "HELPER: netpoller initialized at start: false")
		return string(out)
	}
	out := run("nowarm")
	require.Regexp(t, `new fds after first network use: .*anon_inode:\[eventpoll\]`, out)
	require.Regexp(t, `new fds after first network use: .*anon_inode:\[eventfd\]`, out)
	require.Contains(t, run("warm"), "HELPER: no leak reported")
}

// ---------------------------------------------------------------------------
// Growth.
// ---------------------------------------------------------------------------

func TestGrowth_LeakPerIteration_Caught(t *testing.T) {
	dogfood(t)
	stop := make(chan struct{})
	defer close(stop)
	var files []*os.File
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()

	f := newFakeTB(t)
	CheckGrowth(f, Growth{Iterations: 20, MaxGoroutines: 2, MaxFDs: 2, Settle: failingSettle}, func(i int) {
		go leakyWorker(stop)
		file, err := os.Open(os.DevNull)
		require.NoError(t, err)
		files = append(files, file)
	})

	msg := f.requireOneError(t)
	require.Contains(t, msg, "goroutines: +20 (1.00 per iteration, max allowed 2)")
	require.Contains(t, msg, "20 x [chan receive] top: github.com/buildbuddy-io/buildbuddy/server/testutil/testleak.leakyWorker")
	if fdsSupported() {
		require.Contains(t, msg, "file descriptors: +20 (1.00 per iteration, max allowed 2)")
		require.Contains(t, msg, "-> /dev/null")
		require.Contains(t, msg, " 1:+1,+1 2:+2,+2 3:+3,+3")
	}
}

// pool lazily starts up to size long-lived workers.
type pool struct {
	size    int
	workers int
	stop    chan struct{}
	wg      sync.WaitGroup
}

func (p *pool) use() {
	if p.workers < p.size {
		p.workers++
		p.wg.Go(func() { leakyWorker(p.stop) })
	}
}

func (p *pool) Close() {
	close(p.stop)
	p.wg.Wait()
}

func TestGrowth_BoundedPool_Accepted(t *testing.T) {
	dogfood(t)
	p := &pool{size: 4, stop: make(chan struct{})}
	defer p.Close()
	f := newFakeTB(t)
	// The first iteration (warmup) starts one worker, and the next three
	// start the rest.
	CheckGrowth(f, Growth{Iterations: 50, MaxGoroutines: 3}, func(int) { p.use() })
	f.requireNoErrors(t)
	require.Equal(t, 4, p.workers)
}

func TestGrowth_DelayedCleanup_Accepted(t *testing.T) {
	dogfood(t)
	f := newFakeTB(t)
	CheckGrowth(f, Growth{Iterations: 10}, func(int) {
		stop := make(chan struct{})
		go delayedWorker(stop, 200*time.Millisecond)
		close(stop)
		file, err := os.Open(os.DevNull)
		require.NoError(t, err)
		time.AfterFunc(200*time.Millisecond, func() { file.Close() })
	})
	f.requireNoErrors(t)
}

// ---------------------------------------------------------------------------
// gRPC.
// ---------------------------------------------------------------------------

type grpcServer struct {
	server *grpc.Server
	addr   string
}

func startGRPCServer(t *testing.T) *grpcServer {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := grpc.NewServer()
	healthpb.RegisterHealthServer(s, health.NewServer())
	go s.Serve(lis)
	return &grpcServer{server: s, addr: lis.Addr().String()}
}

func dialAndCall(t *testing.T, addr string) *grpc.ClientConn {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	return conn
}

func TestGRPC_ClosedConnAndServer_Accepted(t *testing.T) {
	dogfood(t)
	f := newFakeTB(t)
	Check(f)
	CheckFDs(f)
	s := startGRPCServer(t)
	f.Cleanup(s.server.Stop)
	conn := dialAndCall(t, s.addr)
	f.Cleanup(func() { conn.Close() })
	f.runCleanups()
	f.requireNoErrors(t)
}

func TestGRPC_UnclosedClientConn_Caught(t *testing.T) {
	dogfood(t)
	// The server is long-lived, and stopped after the check.
	s := startGRPCServer(t)
	defer s.server.Stop()

	f := newFakeTB(t)
	checkGoroutines(f, failingSettle)
	CheckFDs(f, FDSettle(failingSettle))
	conn := dialAndCall(t, s.addr)
	defer conn.Close()
	f.runCleanups()

	msg := f.allErrors()
	t.Logf("leak report (expected):\n%s", msg)
	require.Contains(t, msg, "leaked goroutines")
	require.Contains(t, msg, "google.golang.org/grpc/internal/transport")
	if fdsSupported() {
		require.Contains(t, msg, "leaked file descriptors")
		// Both ends of the connection, since the server is in-process.
		require.Contains(t, msg, fmt.Sprintf("-> %s ESTABLISHED", s.addr))
		require.Contains(t, msg, fmt.Sprintf("tcp %s -> ", s.addr))
	}
}

// The long-lived server stays up; each operation opens a client connection.
func TestGRPC_Growth(t *testing.T) {
	s := startGRPCServer(t)
	defer s.server.Stop()

	t.Run("closed", func(t *testing.T) {
		dogfood(t)
		f := newFakeTB(t)
		CheckGrowth(f, Growth{Iterations: 10}, func(int) {
			dialAndCall(t, s.addr).Close()
		})
		f.requireNoErrors(t)
	})
	t.Run("unclosed", func(t *testing.T) {
		dogfood(t)
		var conns []*grpc.ClientConn
		defer func() {
			for _, c := range conns {
				c.Close()
			}
		}()
		f := newFakeTB(t)
		CheckGrowth(f, Growth{Iterations: 10, MaxGoroutines: 5, MaxFDs: 5, Settle: failingSettle}, func(int) {
			conns = append(conns, dialAndCall(t, s.addr))
		})
		msg := f.requireOneError(t)
		require.Contains(t, msg, "google.golang.org/grpc")
		if fdsSupported() {
			// Client and server end of each connection.
			require.Contains(t, msg, "file descriptors: +20 (2.00 per iteration")
		}
	})
}

// Make sure the helpers in this file are exercised on the platforms where
// FDs aren't supported, too.
func TestNoFDsOnOtherPlatforms(t *testing.T) {
	if runtime.GOOS == "linux" {
		require.True(t, fdsSupported())
		return
	}
	f := newFakeTB(t)
	CheckFDs(f)
	f.runCleanups()
	f.requireNoErrors(t)
}

// A single worker and file are replaced on every operation. Identity changes
// are not accumulation: the live owner's resource usage stays constant.
func TestGrowth_BoundedReplacement_Accepted(t *testing.T) {
	dogfood(t)
	var stop, done chan struct{}
	var file *os.File
	closeResources := func() {
		if stop != nil {
			close(stop)
			<-done
		}
		if file != nil {
			require.NoError(t, file.Close())
		}
	}
	defer closeResources()
	f := newFakeTB(t)
	CheckGrowth(f, Growth{Iterations: 10, Settle: failingSettle}, func(int) {
		closeResources()
		stop, done = make(chan struct{}), make(chan struct{})
		go func(stop, done chan struct{}) {
			defer close(done)
			leakyWorker(stop)
		}(stop, done)
		var err error
		file, err = os.CreateTemp(t.TempDir(), "replacement")
		require.NoError(t, err)
	})
	f.requireNoErrors(t)
}
