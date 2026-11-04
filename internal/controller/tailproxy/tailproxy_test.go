package tailproxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/controller/internalapi"
	"github.com/denyszorinets/damson/internal/controller/internalauth"
	"github.com/denyszorinets/damson/internal/store"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

const secret = "0123456789abcdef0123456789abcdef-cluster"

var now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type fakeLocal struct{ err error }

func (f fakeLocal) Subscribe(context.Context, string, int64) (<-chan api.LogLine, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan api.LogLine)
	close(ch)
	return ch, nil
}

type fakeRegistry struct {
	ex    store.Execution
	peers []store.ControllerInstance
}

func (f fakeRegistry) GetExecution(context.Context, string) (store.Execution, error) {
	return f.ex, nil
}
func (f fakeRegistry) ListControllerInstances(context.Context) ([]store.ControllerInstance, error) {
	return f.peers, nil
}

// ownerTail is the owner's buffer behind a real internal server.
type ownerTail struct {
	attempt string
	lines   chan api.LogLine
}

func (o *ownerTail) Attempt(string) string { return o.attempt }
func (o *ownerTail) Subscribe(ctx context.Context, _ string, after int64) (<-chan api.LogLine, error) {
	out := make(chan api.LogLine, 16)
	go func() {
		defer close(out)
		for {
			select {
			case l, ok := <-o.lines:
				if !ok {
					return
				}
				if l.Seq > after {
					out <- l
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type fixture struct {
	tail  *Tail
	owner *ownerTail
	srv   *httptest.Server
	reg   *fakeRegistry
	logs  *syncBuf
}

func newFixture(t *testing.T, mutate func(*Config)) *fixture {
	t.Helper()
	keys, err := internalauth.NewKeyring(secret, "")
	if err != nil {
		t.Fatal(err)
	}
	owner := &ownerTail{attempt: "a1", lines: make(chan api.LogLine, 16)}
	is, err := internalapi.New(internalapi.Config{Keys: keys, Self: "ctl-B", Tail: owner, Heartbeat: 40 * time.Millisecond, Now: func() time.Time { return now }, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(is.Handler())
	t.Cleanup(srv.Close)
	reg := &fakeRegistry{
		ex:    store.Execution{ID: "e1", State: store.ExecRunning, ControllerID: "ctl-B", AttemptID: "a1"},
		peers: []store.ControllerInstance{{ID: "ctl-B", InternalURL: srv.URL, LastSeen: now}},
	}
	logs := &syncBuf{}
	cfg := Config{
		Local: fakeLocal{err: api.ErrNoLiveTail}, Store: reg, Self: "ctl-A", Keys: keys,
		Now: func() time.Time { return now }, Idle: time.Second, Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	tl, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{tail: tl, owner: owner, srv: srv, reg: reg, logs: logs}
}

func recv(t *testing.T, ch <-chan api.LogLine) (api.LogLine, bool) {
	t.Helper()
	select {
	case l, ok := <-ch:
		return l, ok
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a line")
		return api.LogLine{}, false
	}
}

func TestProxiesTheOwnersTail(t *testing.T) {
	f := newFixture(t, nil)
	f.owner.lines <- api.LogLine{Seq: 1, Line: "skipped"}
	f.owner.lines <- api.LogLine{Seq: 2, Stream: "out", Line: "hello"}
	ch, err := f.tail.Subscribe(context.Background(), "e1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := recv(t, ch); !ok || l.Seq != 2 || l.Line != "hello" || l.Stream != "out" {
		t.Fatalf("first line = %+v, %v", l, ok)
	}
	f.owner.lines <- api.LogLine{Seq: 3, Line: "world"}
	if l, _ := recv(t, ch); l.Seq != 3 {
		t.Fatalf("second line = %+v", l)
	}
	close(f.owner.lines) // the owner's execution ended
	if _, ok := recv(t, ch); ok {
		t.Fatal("channel still open after the owner ended the tail")
	}
	if strings.Contains(f.logs.String(), secret) {
		t.Fatalf("secret in the log: %s", f.logs.String())
	}
}

func TestLocalTailWins(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Local = fakeLocal{} })
	ch, err := f.tail.Subscribe(context.Background(), "e1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := <-ch; ok {
		t.Fatal("expected the local (closed) channel")
	}
}

func TestNoLiveTailWhenOwnerIsNotUsable(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(f *fixture)
	}{
		{"execution finished", func(f *fixture) { f.reg.ex.State = store.ExecSucceeded }},
		{"owned by this controller", func(f *fixture) { f.reg.ex.ControllerID = "ctl-A" }},
		{"unowned", func(f *fixture) { f.reg.ex.ControllerID = "" }},
		{"owner row missing", func(f *fixture) { f.reg.peers = nil }},
		{"owner silent", func(f *fixture) { f.reg.peers[0].LastSeen = now.Add(-time.Minute) }},
		{"bad internal url", func(f *fixture) { f.reg.peers[0].InternalURL = "ftp://user:pw@evil/x?y" }},
		{"other attempt on the owner", func(f *fixture) { f.reg.ex.AttemptID = "a2" }},
		{"owner unreachable", func(f *fixture) { f.srv.Close() }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			tc.mutate(f)
			if _, err := f.tail.Subscribe(context.Background(), "e1", 0); !errors.Is(err, api.ErrNoLiveTail) {
				t.Fatalf("Subscribe = %v; want ErrNoLiveTail", err)
			}
		})
	}
}

func TestNeverProxiesWithoutKeys(t *testing.T) {
	var hits atomic.Int32
	f := newFixture(t, func(c *Config) { c.Keys = nil })
	f.srv.Config.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	if _, err := f.tail.Subscribe(context.Background(), "e1", 0); !errors.Is(err, api.ErrNoLiveTail) || hits.Load() != 0 {
		t.Fatalf("Subscribe = %v with %d hits", err, hits.Load())
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var elsewhere atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer target.Close()
	f := newFixture(t, nil)
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	})
	if _, err := f.tail.Subscribe(context.Background(), "e1", 0); !errors.Is(err, api.ErrNoLiveTail) {
		t.Fatalf("Subscribe = %v", err)
	}
	if elsewhere.Load() != 0 {
		t.Fatal("the redirect was followed")
	}
}

func TestSilentOwnerIsDropped(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Idle = 150 * time.Millisecond })
	// A server that answers 200 and then says nothing at all.
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ch, err := f.tail.Subscribe(context.Background(), "e1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := recv(t, ch); ok {
		t.Fatal("got a line from a silent owner")
	}
}

func TestCancelStopsTheRelay(t *testing.T) {
	f := newFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := f.tail.Subscribe(ctx, "e1", 0)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, ok := recv(t, ch); ok {
		t.Fatal("line after cancel")
	}
}
