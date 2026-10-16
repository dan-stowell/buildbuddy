package scheduler_server

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/action_merger"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testredis"
	"github.com/buildbuddy-io/buildbuddy/server/remote_cache/digest"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/prefix"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	scpb "github.com/buildbuddy-io/buildbuddy/proto/scheduler"
)

// lostReplyInjector decides which Redis replies the proxies drop. When a
// command matching the armed predicate is executed successfully by Redis, the
// proxy discards the reply and closes the client connection, simulating a
// connection reset after Redis has already applied the command.
type lostReplyInjector struct {
	mu        sync.Mutex
	match     func(args []string) bool
	remaining int
	dropped   int
	seen      []string
}

func (i *lostReplyInjector) arm(n int, match func(args []string) bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.match = match
	i.remaining = n
	i.dropped = 0
	i.seen = nil
}

func (i *lostReplyInjector) shouldDrop(args []string, reply []byte) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.match == nil || !i.match(args) {
		return false
	}
	i.seen = append(i.seen, fmt.Sprintf("%s -> %q", strings.Join(abbrev(args), " "), reply))
	// Only drop replies of commands that Redis actually executed.
	if i.remaining == 0 || len(reply) == 0 || reply[0] == '-' {
		return false
	}
	i.remaining--
	i.dropped++
	return true
}

func (i *lostReplyInjector) Dropped() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.dropped
}

// Seen returns the matching commands (including retries) and the replies that
// Redis sent for them.
func (i *lostReplyInjector) Seen() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.seen...)
}

func abbrev(args []string) []string {
	out := make([]string, len(args))
	for j, a := range args {
		if len(a) > 40 {
			a = a[:40] + "..."
		}
		out[j] = strconv.Quote(a)
	}
	return out
}

// lostReplyProxy is a TCP proxy in front of a Redis server which speaks just
// enough RESP to forward one command and one reply at a time.
type lostReplyProxy struct {
	t        testing.TB
	backend  string
	injector *lostReplyInjector
	lis      net.Listener

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
}

func startLostReplyProxy(t testing.TB, backend string, injector *lostReplyInjector) *lostReplyProxy {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &lostReplyProxy{
		t:        t,
		backend:  backend,
		injector: injector,
		lis:      lis,
		conns:    map[net.Conn]struct{}{},
	}
	p.wg.Add(1)
	go p.acceptLoop()
	t.Cleanup(func() {
		lis.Close()
		p.mu.Lock()
		for c := range p.conns {
			c.Close()
		}
		p.mu.Unlock()
		p.wg.Wait()
	})
	return p
}

func (p *lostReplyProxy) Addr() string { return p.lis.Addr().String() }

func (p *lostReplyProxy) track(c net.Conn) {
	p.mu.Lock()
	p.conns[c] = struct{}{}
	p.mu.Unlock()
}

func (p *lostReplyProxy) acceptLoop() {
	defer p.wg.Done()
	for {
		client, err := p.lis.Accept()
		if err != nil {
			return
		}
		server, err := net.Dial("tcp", p.backend)
		if err != nil {
			client.Close()
			continue
		}
		p.track(client)
		p.track(server)
		p.wg.Go(func() {
			defer client.Close()
			defer server.Close()
			p.serve(client, server)
		})
	}
}

func (p *lostReplyProxy) serve(client, server net.Conn) {
	cr := bufio.NewReader(client)
	sr := bufio.NewReader(server)
	for {
		raw, args, err := readRESP(cr)
		if err != nil {
			return
		}
		if _, err := server.Write(raw); err != nil {
			return
		}
		if len(args) > 0 && strings.Contains(strings.ToLower(args[0]), "subscribe") {
			// Pub/sub connections receive unsolicited messages; just pipe
			// them through.
			p.wg.Go(func() {
				io.Copy(server, cr)
				server.Close()
			})
			io.Copy(client, sr)
			return
		}
		reply, _, err := readRESP(sr)
		if err != nil {
			return
		}
		if p.injector.shouldDrop(args, reply) {
			// Redis executed the command, but the client never sees the
			// reply: the connection is reset instead.
			if tc, ok := client.(*net.TCPConn); ok {
				tc.SetLinger(0)
			}
			return
		}
		if _, err := client.Write(reply); err != nil {
			return
		}
	}
}

// readRESP reads one RESP value and returns its raw bytes. If the value is an
// array of bulk strings (i.e. a command), its elements are also returned.
func readRESP(r *bufio.Reader) ([]byte, []string, error) {
	var buf bytes.Buffer
	var args []string
	if err := readRESPValue(r, &buf, &args, true); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), args, nil
}

func readRESPValue(r *bufio.Reader, buf *bytes.Buffer, args *[]string, top bool) error {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return err
	}
	buf.Write(line)
	if len(line) < 3 {
		return fmt.Errorf("short RESP line %q", line)
	}
	body := string(line[1 : len(line)-2])
	switch line[0] {
	case '+', '-', ':', '_', ',', '#', '(':
		return nil
	case '$', '=', '!':
		n, err := strconv.Atoi(body)
		if err != nil {
			return err
		}
		if n < 0 {
			return nil
		}
		data := make([]byte, n+2)
		if _, err := io.ReadFull(r, data); err != nil {
			return err
		}
		buf.Write(data)
		if args != nil && !top {
			*args = append(*args, string(data[:n]))
		}
		return nil
	case '*', '~', '>', '%', '|':
		n, err := strconv.Atoi(body)
		if err != nil {
			return err
		}
		if line[0] == '%' || line[0] == '|' {
			n *= 2
		}
		var childArgs *[]string
		if top && line[0] == '*' {
			childArgs = args
		}
		for range max(n, 0) {
			if err := readRESPValue(r, buf, childArgs, false); err != nil {
				return err
			}
		}
		if line[0] == '|' {
			// Attributes are followed by the actual value.
			return readRESPValue(r, buf, args, top)
		}
		return nil
	default:
		return fmt.Errorf("unexpected RESP type %q", line[0])
	}
}

// newLostReplyEnv starts one Redis server (or two, as a Ring) behind
// lost-reply proxies and returns a scheduler test env whose Redis client goes
// through the proxies. The client uses the go-redis default retry settings.
func newLostReplyEnv(t *testing.T, ring bool) (*testenv.TestEnv, context.Context, *lostReplyInjector) {
	injector := &lostReplyInjector{}
	opts := &schedulerOpts{
		redisClientFactory: func(t *testing.T) redis.UniversalClient {
			var rdb redis.UniversalClient
			if ring {
				addrs := map[string]string{}
				for i := range 2 {
					h := testredis.StartTCP(t)
					addrs[fmt.Sprintf("shard%d", i)] = startLostReplyProxy(t, h.Target, injector).Addr()
				}
				rdb = redis.NewRing(&redis.RingOptions{Addrs: addrs})
			} else {
				h := testredis.StartTCP(t)
				rdb = redis.NewClient(&redis.Options{Addr: startLostReplyProxy(t, h.Target, injector).Addr()})
			}
			t.Cleanup(func() { rdb.Close() })
			return rdb
		},
	}
	if ring {
		// The go-redis v8 Ring heartbeat goroutine outlives Close.
		opts.leakOptions = []goleak.Option{goleak.IgnoreTopFunction("github.com/go-redis/redis/v8.(*ringShards).Heartbeat")}
	}
	env, ctx := getEnv(t, opts, "user1")
	ctx, err := prefix.AttachUserPrefixToContext(ctx, env.GetAuthenticator())
	require.NoError(t, err)
	return env, ctx, injector
}

// forEachClientMode runs f with a simple and a Ring client, each with and
// without a lost reply. The runs without a lost reply check the harness.
func forEachClientMode(t *testing.T, f func(t *testing.T, ring bool, numDrops int)) {
	for _, ring := range []bool{false, true} {
		for _, numDrops := range []int{0, 1} {
			name := "simple"
			if ring {
				name = "ring"
			}
			if numDrops > 0 {
				name += "_lost_reply"
			} else {
				name += "_no_fault"
			}
			t.Run(name, func(t *testing.T) { f(t, ring, numDrops) })
		}
	}
}

// The reply to the task HSET is lost after Redis applied it, and the client
// library transparently re-sends the HSET.
func TestRedisLostReply_ScheduleTask(t *testing.T) {
	forEachClientMode(t, func(t *testing.T, ring bool, numDrops int) {
		env, ctx, injector := newLostReplyEnv(t, ring)
		s := env.GetSchedulerService().(*SchedulerServer)
		fe := newFakeExecutor(ctx, t, env.GetSchedulerClient())
		fe.Register()

		req := newScheduleRequest(ctx, t, env, scheduleOpts{})
		taskID := req.GetTaskId()
		taskKey := s.redisKeyForTask(taskID)
		injector.arm(numDrops, func(args []string) bool {
			return len(args) >= 2 && strings.EqualFold(args[0], "hset") && args[1] == taskKey
		})

		_, scheduleErr := s.ScheduleTask(ctx, req)
		ttl, ttlErr := s.rdb.TTL(ctx, taskKey).Result()
		for _, c := range injector.Seen() {
			t.Logf("redis saw: %s", c)
		}
		t.Logf("ScheduleTask error: %v; task key TTL: %v (err: %v)", scheduleErr, ttl, ttlErr)

		require.Equal(t, numDrops, injector.Dropped(), "unexpected number of lost HSET replies")
		require.NoError(t, scheduleErr, "ScheduleTask should succeed when only the HSET reply was lost")
		require.NoError(t, ttlErr)
		require.Greater(t, ttl, time.Duration(0), "task key should have a TTL")
		fe.WaitForTask(taskID)
	})
}

// The reply to the finalize (delete claimed task) script is lost after Redis
// ran it, and the client library transparently re-runs the script.
func TestRedisLostReply_FinalizeTask(t *testing.T) {
	forEachClientMode(t, func(t *testing.T, ring bool, numDrops int) {
		env, ctx, injector := newLostReplyEnv(t, ring)
		s := env.GetSchedulerService().(*SchedulerServer)
		fe1 := newFakeExecutor(ctx, t, env.GetSchedulerClient())
		fe1.Register()
		fe2 := newFakeExecutor(ctx, t, env.GetSchedulerClient())
		fe2.node.SupportsProactiveCancellation = true
		fe2.Register()

		// Record action-merging state for the execution, like the execution
		// server does before scheduling it.
		ad := digest.NewCASResourceName(&repb.Digest{Hash: strings.Repeat("ab", 32), SizeBytes: 1}, "", repb.DigestFunction_SHA256)
		executionID, op := action_merger.GetOrCreateExecutionID(ctx, s.rdb, s, ad, false /*=doNotCache*/)
		require.Equal(t, action_merger.New, op)

		req := newScheduleRequest(ctx, t, env, scheduleOpts{})
		req.TaskId = executionID
		_, err := s.ScheduleTask(ctx, req)
		require.NoError(t, err)
		taskID := executionID
		// Both executors are probed (probesPerTask > number of executors).
		fe1.WaitForTask(taskID)
		fe2.WaitForTask(taskID)
		lease := fe1.Claim(taskID)

		taskKey := s.redisKeyForTask(taskID)
		deleteSHA := redisDeleteClaimedTask.Hash()
		injector.arm(numDrops, func(args []string) bool {
			if len(args) < 4 || args[3] != taskKey {
				return false
			}
			cmd := strings.ToLower(args[0])
			return (cmd == "evalsha" && args[1] == deleteSHA) ||
				(cmd == "eval" && strings.Contains(args[1], `return redis.call("del", KEYS[1])`))
		})

		require.NoError(t, lease.stream.Send(&scpb.LeaseTaskRequest{TaskId: taskID, Finalize: true}))
		rsp, err := lease.stream.Recv()
		require.NoError(t, err)

		exists, existsErr := s.rdb.Exists(ctx, taskKey).Result()
		// Wait for proactive cancellation of fe2's queued reservation.
		cancelled := false
		deadline := time.After(3 * time.Second)
	wait:
		for {
			select {
			case msg := <-fe2.schedulerMessages:
				if msg.GetCancelTaskReservationRequest().GetTaskId() == taskID {
					cancelled = true
					break wait
				}
			case <-deadline:
				break wait
			}
		}
		// A later identical action should not find action-merging state
		// pointing at the finished execution. (In this test only the
		// scheduler cleans up action-merging state; the execution server
		// also deletes it when the executor publishes a completed result.)
		start := time.Now()
		laterID, laterOp := action_merger.GetOrCreateExecutionID(ctx, s.rdb, s, ad, false /*=doNotCache*/)
		laterLatency := time.Since(start)

		for _, c := range injector.Seen() {
			t.Logf("redis saw: %s", c)
		}
		t.Logf("finalize ClosedCleanly=%t; task key exists=%d (err: %v); fe2 got proactive cancellation=%t", rsp.GetClosedCleanly(), exists, existsErr, cancelled)
		t.Logf("later identical action: op=%v (new execution: %t) after %s", laterOp, laterID != executionID, laterLatency)

		require.Equal(t, numDrops, injector.Dropped(), "unexpected number of lost script replies")
		require.NoError(t, existsErr)
		require.Equal(t, int64(0), exists, "task should have been deleted by the first script run")
		require.True(t, rsp.GetClosedCleanly(), "finalize of a deleted task should close cleanly")
		require.True(t, cancelled, "other executor's reservation should be proactively cancelled")
		require.Equal(t, action_merger.New, laterOp)
		require.Less(t, laterLatency, 1*time.Second, "later identical action should not wait on stale action-merging state")
	})
}
