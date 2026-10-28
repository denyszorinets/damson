package controller

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// fakeClock is a manual clock; tickers fire only through tick.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers map[time.Duration][]*fakeTicker
}

type fakeTicker struct {
	c chan time.Time
}

func (f *fakeTicker) C() <-chan time.Time { return f.c }
func (f *fakeTicker) Stop()               {}

func newFakeClock() *fakeClock {
	return &fakeClock{now: t0, tickers: make(map[time.Duration][]*fakeTicker)}
}

func (c *fakeClock) Clock() Clock {
	return Clock{
		Now: func() time.Time {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.now
		},
		NewTicker: func(d time.Duration) Ticker {
			c.mu.Lock()
			defer c.mu.Unlock()
			t := &fakeTicker{c: make(chan time.Time, 1)}
			c.tickers[d] = append(c.tickers[d], t)
			return t
		},
	}
}

// advance moves the clock and fires every ticker created for d.
func (c *fakeClock) advance(d time.Duration) time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	ts := append([]*fakeTicker(nil), c.tickers[d]...)
	c.mu.Unlock()
	for _, t := range ts {
		select {
		case t.c <- now:
		default:
		}
	}
	return now
}

// syncBuf is a goroutine-safe log sink.
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

// rig is a started gateway behind an httptest server with one pool and token.
type rig struct {
	t     *testing.T
	st    store.Store
	gw    *Gateway
	srv   *httptest.Server
	clock *fakeClock
	logs  *syncBuf
	pool  store.Pool
	token string // plaintext; the only copy
}

type rigOpt func(*Config)

func newRig(t *testing.T, st store.Store, opts ...rigOpt) *rig {
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
	r := &rig{t: t, st: st, clock: newFakeClock(), logs: &syncBuf{}, pool: pool, token: plain}
	cfg := Config{
		Store: st, ControllerID: "ctl-1", InternalURL: "http://ctl-1:8080", InsecureDev: true,
		Clock: r.clock.Clock(), Logger: slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		HelloTimeout: 2 * time.Second, ReconnectJitter: 1500 * time.Millisecond,
	}
	for _, o := range opts {
		o(&cfg)
	}
	r.gw, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(ConnectPath, r.gw.Handler())
	r.srv = httptest.NewServer(mux)
	if err := r.gw.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.stop()
		r.srv.Close()
	})
	return r
}

func (r *rig) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.gw.Stop(ctx)
}

func (r *rig) wsURL() string { return "ws" + strings.TrimPrefix(r.srv.URL, "http") + ConnectPath }

// agent is a real protocol client: a WebSocket and a Peer, as an agent builds.
type agent struct {
	id       string
	peer     *protocol.Peer
	runDone  chan struct{}
	reconnCh chan protocol.Reconnect
}

// dial connects with the rig's valid token unless headers override it.
func (r *rig) dial(id string) *agent {
	r.t.Helper()
	a, resp, err := r.tryDial(id, http.Header{
		"Authorization":     {"Bearer " + r.token},
		"X-Damson-Agent-Id": {id},
	})
	if err != nil {
		r.t.Fatalf("dial %s: %v (status %v)", id, err, resp)
	}
	return a
}

func (r *rig) tryDial(id string, h http.Header) (*agent, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, r.wsURL(), &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		return nil, resp, err
	}
	a := &agent{id: id, runDone: make(chan struct{}), reconnCh: make(chan protocol.Reconnect, 4)}
	a.peer = protocol.NewPeer(protocol.NewWSConn(ws), slog.New(slog.DiscardHandler))
	a.peer.Handle(protocol.MethodAgentReconnect, func(_ context.Context, p protocol.Params) (any, error) {
		var rc protocol.Reconnect
		if err := p.Decode(&rc); err != nil {
			return nil, err
		}
		a.reconnCh <- rc
		return nil, nil
	})
	go func() {
		defer close(a.runDone)
		_ = a.peer.Run(context.Background())
	}()
	r.t.Cleanup(a.close)
	return a, resp, nil
}

func (a *agent) close() {
	_ = a.peer.Close()
	<-a.runDone
}

func (a *agent) hello(req protocol.HelloRequest) (protocol.HelloResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var resp protocol.HelloResponse
	err := a.peer.Call(ctx, protocol.MethodAgentHello, req, &resp)
	return resp, err
}

func (a *agent) heartbeat(hb protocol.Heartbeat) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.peer.Notify(ctx, protocol.MethodAgentHeartbeat, hb); err != nil {
		panic(err)
	}
}

func defaultHello(seq uint64) protocol.HelloRequest {
	return protocol.HelloRequest{
		Protocol: protocol.ProtocolVersion, SessionSeq: seq, AgentVersion: "1.2.3", Pool: "default",
		Labels: map[string]string{"os": "linux"}, MaxConcurrent: 4,
		Capabilities: []string{"bin:git"},
	}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
