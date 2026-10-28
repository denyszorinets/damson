package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/controller"
	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// ctl is a controller gateway behind a real listener whose address survives
// a restart.
type ctl struct {
	t    *testing.T
	st   store.Store
	id   string
	addr string
	gw   *controller.Gateway
	srv  *http.Server
	hits atomic.Int64 // upgrade requests seen, across restarts
}

func startCtl(t *testing.T, st store.Store, addr string) *ctl {
	t.Helper()
	c := &ctl{t: t, st: st, id: "ctl-1"}
	c.start(addr)
	t.Cleanup(c.shutdown)
	return c
}

func (c *ctl) start(addr string) {
	c.t.Helper()
	gw, err := controller.New(controller.Config{
		Store: c.st, ControllerID: c.id, InsecureDev: true, Logger: slog.New(slog.DiscardHandler),
		HeartbeatInterval: 20 * time.Millisecond, FlushInterval: 20 * time.Millisecond,
		ReconnectJitter: 20 * time.Millisecond,
	})
	if err != nil {
		c.t.Fatal(err)
	}
	if err := gw.Start(context.Background()); err != nil {
		c.t.Fatal(err)
	}
	var ln net.Listener
	for range 100 { // the port may linger briefly after a restart
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		c.t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(controller.ConnectPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits.Add(1)
		gw.Handler().ServeHTTP(w, r)
	}))
	c.gw, c.addr = gw, ln.Addr().String()
	c.srv = &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = c.srv.Serve(ln) }()
}

func (c *ctl) url() string { return "ws://" + c.addr }

// shutdown stops the gateway gracefully (agents get agent.reconnect) and the listener.
func (c *ctl) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.gw.Stop(ctx)
	_ = c.srv.Close()
}

func (c *ctl) restart() {
	c.shutdown()
	c.start(c.addr)
}

type fakeSampler struct{ n atomic.Int64 }

func (f *fakeSampler) Sample() Telemetry {
	f.n.Add(1)
	return Telemetry{CPUPercent: 12.5, MemoryBytes: 1 << 30, Load1: 0.75}
}

type fakePlugin struct{ m pluginsdk.Manifest }

func (p fakePlugin) Manifest() pluginsdk.Manifest                  { return p.m }
func (fakePlugin) Validate(context.Context, json.RawMessage) error { return nil }
func (fakePlugin) Execute(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
	return pluginsdk.Result{}, errors.New("not implemented")
}

type fixture struct {
	t     *testing.T
	st    store.Store
	pool  store.Pool
	token string
	ctl   *ctl
}

func newFixture(t *testing.T, st store.Store) *fixture {
	t.Helper()
	ctx := context.Background()
	pool, err := st.CreatePool(ctx, "default", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	plain, hash, err := tokens.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePoolToken(ctx, pool.ID, "t", hash, t0); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, st: st, pool: pool, token: plain, ctl: startCtl(t, st, "127.0.0.1:0")}
}

type opt func(*Config)

// agent starts an agent against the fixture's controller with fast backoff.
func (f *fixture) agent(id string, opts ...opt) (*Agent, *syncBuf) {
	f.t.Helper()
	logs := &syncBuf{}
	cfg := Config{
		ControllerURL: f.ctl.url(), Token: f.token, AgentID: id, Pool: "default", Slots: 4,
		Logger:      slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Sampler:     &fakeSampler{},
		BackoffBase: 5 * time.Millisecond, BackoffMax: 40 * time.Millisecond,
		LookPath: func(string) (string, error) { return "", errors.New("none") },
	}
	for _, o := range opts {
		o(&cfg)
	}
	a, err := New(cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.Stop(ctx); err != nil {
			f.t.Errorf("stopping agent: %v", err)
		}
	})
	return a, logs
}

func (f *fixture) storedAgent(id string) (store.Agent, bool) {
	a, err := f.st.GetAgent(context.Background(), id)
	return a, err == nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func connectedAtController(f *fixture, id string) bool {
	for _, c := range f.ctl.gw.Connected() {
		if c == id {
			return true
		}
	}
	return false
}

func heartbeatOf(t *testing.T, a store.Agent) protocol.Heartbeat {
	t.Helper()
	var hb protocol.Heartbeat
	if len(a.Telemetry) == 0 {
		return hb
	}
	if err := json.Unmarshal(a.Telemetry, &hb); err != nil {
		t.Fatal(err)
	}
	return hb
}

func TestConnectHelloAndHeartbeat(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFixture(t, st)
		p := fakePlugin{pluginsdk.Manifest{
			Name: "demo", Version: "1.2.3", Title: "Demo", TargetMode: pluginsdk.TargetNone,
			InputSchema: pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
		}}
		a, _ := f.agent("agent-1", func(c *Config) {
			c.Plugins = []pluginsdk.Plugin{p}
			c.Labels = map[string]string{"zone": "a"}
			c.Slots = 3
			c.Version = "9.9.9"
			c.Capabilities = []string{"bin:custom"}
		})
		eventually(t, "agent connected", a.Connected)
		eventually(t, "controller sees agent", func() bool { return connectedAtController(f, "agent-1") })

		var row store.Agent
		eventually(t, "heartbeat telemetry stored", func() bool {
			r, ok := f.storedAgent("agent-1")
			row = r
			return ok && heartbeatOf(t, r).CPUPercent == 12.5
		})
		if row.PoolID != f.pool.ID || row.Capacity != 3 || row.Version != "9.9.9" || row.SessionSeq != 1 {
			t.Errorf("stored agent = %+v", row)
		}
		if row.Labels["zone"] != "a" || row.Labels["os"] == "" {
			t.Errorf("labels = %v", row.Labels)
		}
		if row.Capabilities["demo"] != "1.2.3" {
			t.Errorf("plugin manifest not announced: %v", row.Capabilities)
		}
		if _, ok := row.Capabilities["bin:custom"]; !ok {
			t.Errorf("capability missing: %v", row.Capabilities)
		}
		hb := heartbeatOf(t, row)
		if hb.FreeSlots != 3 || hb.MemoryBytes != 1<<30 || hb.Load1 != 0.75 {
			t.Errorf("heartbeat = %+v", hb)
		}
	})
}

func TestReconnectAfterControllerRestart(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFixture(t, st)
		a, _ := f.agent("agent-1")
		eventually(t, "connected", func() bool { return a.Connected() && connectedAtController(f, "agent-1") })

		// Graceful: the controller sends agent.reconnect and shuts down.
		f.ctl.restart()
		eventually(t, "reconnected after graceful restart", func() bool {
			r, ok := f.storedAgent("agent-1")
			return ok && r.SessionSeq >= 2 && a.Connected() && connectedAtController(f, "agent-1")
		})
		seq := mustAgent(t, f, "agent-1").SessionSeq

		// Crash: the listener vanishes and the socket drops without notice.
		peer, ok := f.ctl.gw.Peer("agent-1")
		if !ok {
			t.Fatal("no peer")
		}
		_ = f.ctl.srv.Close()
		_ = peer.Close()
		eventually(t, "agent notices the loss", func() bool { return !a.Connected() })
		time.Sleep(50 * time.Millisecond) // a few refused dials
		hits := f.ctl.hits.Load()
		f.ctl.restart()
		eventually(t, "reconnected after crash", func() bool {
			r, ok := f.storedAgent("agent-1")
			return ok && r.SessionSeq > seq && a.Connected() && connectedAtController(f, "agent-1")
		})
		if f.ctl.hits.Load() <= hits {
			t.Error("agent did not dial the restarted controller")
		}
		select {
		case <-a.Done():
			t.Fatalf("agent gave up: %v", a.Err())
		default:
		}
	})
}

func mustAgent(t *testing.T, f *fixture, id string) store.Agent {
	t.Helper()
	r, ok := f.storedAgent(id)
	if !ok {
		t.Fatalf("agent %s not stored", id)
	}
	return r
}

func TestFatalAuthFailureDoesNotRetry(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFixture(t, st)
		a, logs := f.agent("agent-1", func(c *Config) { c.Token = "dmp_wrong-token-value" })
		select {
		case <-a.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("agent kept retrying a rejected token")
		}
		var se *StatusError
		if err := a.Err(); !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
			t.Errorf("Err = %v, want HTTP 401", err)
		}
		time.Sleep(100 * time.Millisecond)
		if n := f.ctl.hits.Load(); n != 1 {
			t.Errorf("controller saw %d connection attempts, want 1", n)
		}
		if strings.Contains(logs.String(), "wrong-token-value") {
			t.Errorf("token leaked: %s", logs.String())
		}
	})
}

func TestFatalPoolConflict(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFixture(t, st)
		other, err := st.CreatePool(context.Background(), "other", "", t0)
		if err != nil {
			t.Fatal(err)
		}
		plain, hash, _ := tokens.New()
		if _, err := st.CreatePoolToken(context.Background(), other.ID, "t", hash, t0); err != nil {
			t.Fatal(err)
		}
		first, _ := f.agent("dup")
		eventually(t, "first connected", first.Connected)

		second, _ := f.agent("dup", func(c *Config) { c.Token, c.Pool = plain, "other" })
		select {
		case <-second.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("agent kept retrying a pool conflict")
		}
		var re *protocol.RPCError
		if err := second.Err(); !errors.As(err, &re) || re.Code != protocol.CodePoolConflict {
			t.Errorf("Err = %v, want CodePoolConflict", err)
		}
		if !first.Connected() {
			t.Error("the established agent was disturbed")
		}
	})
}

func TestStaleSessionReconnectsWithHigherSeq(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFixture(t, st)
		// A previous process of the same agent left session_seq 3 behind.
		err := st.UpsertAgentOnHello(context.Background(), store.AgentHello{
			ID: "agent-1", PoolID: f.pool.ID, Protocol: 1, Capacity: 1, ControllerID: "old", SessionSeq: 3,
		}, t0)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := f.agent("agent-1")
		eventually(t, "connected after stale attempts", func() bool {
			r, ok := f.storedAgent("agent-1")
			return ok && r.ControllerID == "ctl-1" && a.Connected()
		})
		if got := mustAgent(t, f, "agent-1").SessionSeq; got != 4 {
			t.Errorf("session_seq = %d, want 4 (first value above the stored 3)", got)
		}
		select {
		case <-a.Done():
			t.Fatalf("stale session was treated as fatal: %v", a.Err())
		default:
		}
	})
}

func TestDrainAndReconnectFromController(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFixture(t, st)
		a, _ := f.agent("agent-1")
		eventually(t, "connected", func() bool { return a.Connected() && connectedAtController(f, "agent-1") })
		peer, _ := f.ctl.gw.Peer("agent-1")

		if err := peer.Notify(context.Background(), protocol.MethodAgentDrain, protocol.Drain{Reason: "test"}); err != nil {
			t.Fatal(err)
		}
		eventually(t, "draining", a.Draining)
		eventually(t, "heartbeat reports no free slots", func() bool {
			r, ok := f.storedAgent("agent-1")
			return ok && r.Telemetry != nil && heartbeatOf(t, r).FreeSlots == 0
		})

		before := mustAgent(t, f, "agent-1").SessionSeq
		if err := peer.Notify(context.Background(), protocol.MethodAgentReconnect, protocol.Reconnect{JitterMS: 10}); err != nil {
			t.Fatal(err)
		}
		eventually(t, "reconnected on request", func() bool {
			return mustAgent(t, f, "agent-1").SessionSeq > before && a.Connected() && connectedAtController(f, "agent-1")
		})
		if a.Draining() {
			t.Error("drain survived a new session")
		}
	})
}

func TestTokenNeverLogged(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newFixture(t, st)
		a, logs := f.agent("agent-1")
		eventually(t, "connected", a.Connected)
		f.ctl.restart() // exercise the reconnect log paths too
		eventually(t, "reconnected", func() bool { return mustAgent(t, f, "agent-1").SessionSeq >= 2 && a.Connected() })
		if !strings.Contains(logs.String(), `"agent_id":"agent-1"`) {
			t.Errorf("log lines lack agent_id: %s", logs.String())
		}
		if strings.Contains(logs.String(), f.token) || strings.Contains(logs.String(), "Bearer") {
			t.Errorf("token leaked into logs: %s", logs.String())
		}
	})
}

// fakeTimer fires at once and records the requested delays.
type timerLog struct {
	mu sync.Mutex
	ds []time.Duration
}

func (l *timerLog) clock() Clock {
	return Clock{
		NewTimer: func(d time.Duration) Timer {
			l.mu.Lock()
			l.ds = append(l.ds, d)
			l.mu.Unlock()
			c := make(chan time.Time, 1)
			c <- time.Time{}
			return fakeTimer{c}
		},
	}
}

func (l *timerLog) delays() []time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]time.Duration(nil), l.ds...)
}

type fakeTimer struct{ c chan time.Time }

func (f fakeTimer) C() <-chan time.Time { return f.c }
func (fakeTimer) Stop()                 {}

// TestReconnectLoopBackoffSequence drives the loop against a dead address
// with a fake clock and maximal jitter: the waits must double from 1s to the
// 30s cap.
func TestReconnectLoopBackoffSequence(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens: every dial is refused

	tl := &timerLog{}
	a, err := New(Config{
		ControllerURL: "ws://" + addr, Token: "t", AgentID: "a", Pool: "p",
		Logger: slog.New(slog.DiscardHandler), Clock: tl.clock(), Rand: func() float64 { return 1 },
		LookPath: func(string) (string, error) { return "", errors.New("none") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "seven backoff waits", func() bool { return len(tl.delays()) >= 7 })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30}
	got := tl.delays()
	for i, w := range want {
		if got[i] != w*time.Second {
			t.Errorf("wait %d = %v, want %v (all: %v)", i, got[i], w*time.Second, got)
		}
	}
}

func TestStopWithoutStartAndTwice(t *testing.T) {
	a, err := New(Config{ControllerURL: "ws://127.0.0.1:1", Token: "t", AgentID: "a", Pool: "p"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.Stop(ctx); err != nil {
		t.Errorf("Stop before Start = %v", err)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx); err == nil {
		t.Error("second Start succeeded")
	}
	for range 2 {
		if err := a.Stop(ctx); err != nil {
			t.Errorf("Stop = %v", err)
		}
	}
}
