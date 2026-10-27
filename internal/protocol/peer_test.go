package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// startPair runs two peers over conn pairs and stops them at test end.
func startPair(t *testing.T, ca, cb Conn) (*Peer, *Peer) {
	t.Helper()
	a, b := NewPeer(ca, quiet()), NewPeer(cb, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, p := range []*Peer{a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.Run(ctx) }()
	}
	t.Cleanup(func() {
		cancel()
		_ = a.Close()
		_ = b.Close()
		wg.Wait()
	})
	return a, b
}

// startOne runs a single peer on conn, leaving the other end raw.
func startOne(t *testing.T, conn Conn) *Peer {
	t.Helper()
	p := NewPeer(conn, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); _ = p.Close(); <-done })
	return p
}

func pipePair(t *testing.T) (*Peer, *Peer) {
	ca, cb := newPipe()
	return startPair(t, ca, cb)
}

func wsPair(t *testing.T) (*Peer, *Peer) {
	t.Helper()
	srvConn := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		srvConn <- c
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cl, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return startPair(t, NewWSConn(cl), NewWSConn(<-srvConn))
}

var transports = []struct {
	name string
	pair func(*testing.T) (*Peer, *Peer)
}{{"pipe", pipePair}, {"websocket", wsPair}}

func echoHandlers(p *Peer) {
	p.Handle("echo", func(_ context.Context, params Params) (any, error) {
		var v map[string]any
		if err := params.Decode(&v); err != nil {
			return nil, err
		}
		return v, nil
	})
}

func TestPeerConcurrentCallsBothDirections(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			a, b := tr.pair(t)
			echoHandlers(a)
			echoHandlers(b)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			var wg sync.WaitGroup
			errs := make(chan error, 200)
			for _, caller := range []*Peer{a, b} {
				for i := range 50 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						var out map[string]any
						if err := caller.Call(ctx, "echo", map[string]any{"i": i}, &out); err != nil {
							errs <- err
							return
						}
						if fmt.Sprint(out["i"]) != fmt.Sprint(i) {
							errs <- fmt.Errorf("call %d got %v", i, out)
						}
					}()
				}
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Error(err)
			}
		})
	}
}

func TestPeerTypedMethods(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			agent, ctrl := tr.pair(t)
			ctrl.Handle(MethodAgentHello, func(_ context.Context, p Params) (any, error) {
				var req HelloRequest
				if err := p.Decode(&req); err != nil {
					return nil, err
				}
				v, err := Negotiate(req.Protocol)
				if err != nil {
					return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
				}
				return HelloResponse{Protocol: v, HeartbeatIntervalMS: 10000}, nil
			})
			got := make(chan Heartbeat, 1)
			ctrl.Handle(MethodAgentHeartbeat, func(_ context.Context, p Params) (any, error) {
				var hb Heartbeat
				err := p.Decode(&hb)
				got <- hb
				return nil, err
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			var resp HelloResponse
			if err := agent.Call(ctx, MethodAgentHello, HelloRequest{Protocol: 1, Pool: "p"}, &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Protocol != 1 || resp.HeartbeatIntervalMS != 10000 {
				t.Fatalf("resp = %+v", resp)
			}
			err := agent.Call(ctx, MethodAgentHello, HelloRequest{Protocol: 9}, &resp)
			var re *RPCError
			if !errors.As(err, &re) || re.Code != CodeInvalidParams {
				t.Fatalf("bad version err = %v", err)
			}
			if err := agent.Notify(ctx, MethodAgentHeartbeat, Heartbeat{FreeSlots: 4}); err != nil {
				t.Fatal(err)
			}
			select {
			case hb := <-got:
				if hb.FreeSlots != 4 {
					t.Fatalf("hb = %+v", hb)
				}
			case <-ctx.Done():
				t.Fatal("notification not delivered")
			}
		})
	}
}

func TestPeerNotificationOrder(t *testing.T) {
	a, b := pipePair(t)
	var mu sync.Mutex
	var seen []int
	done := make(chan struct{})
	b.Handle("n", func(_ context.Context, p Params) (any, error) {
		var i int
		_ = p.Decode(&i)
		mu.Lock()
		seen = append(seen, i)
		if len(seen) == 100 {
			close(done)
		}
		mu.Unlock()
		return nil, nil
	})
	for i := range 100 {
		if err := a.Notify(context.Background(), "n", i); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	for i, v := range seen {
		if v != i {
			t.Fatalf("out of order at %d: %v", i, seen)
		}
	}
}

func TestPeerErrors(t *testing.T) {
	a, b := pipePair(t)
	b.Handle("plain", func(context.Context, Params) (any, error) { return nil, errTest })
	b.Handle("rpc", func(context.Context, Params) (any, error) {
		return nil, &RPCError{Code: -32001, Message: "custom"}
	})
	tests := []struct {
		method   string
		wantCode int
	}{
		{"missing", CodeMethodNotFound},
		{"plain", CodeInternalError},
		{"rpc", -32001},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			err := a.Call(context.Background(), tt.method, nil, nil)
			var re *RPCError
			if !errors.As(err, &re) || re.Code != tt.wantCode {
				t.Fatalf("err = %v, want code %d", err, tt.wantCode)
			}
		})
	}
	// Unknown notifications are ignored and the peer keeps working.
	if err := a.Notify(context.Background(), "nobody", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Call(context.Background(), "rpc", nil, nil); err == nil {
		t.Fatal("peer stopped serving")
	}
}

func TestPeerMalformedFrameKeepsConnection(t *testing.T) {
	ca, cb := newPipe()
	b := startOne(t, cb)
	echoHandlers(b)
	ctx := context.Background()
	if err := ca.WriteFrame(ctx, []byte{0xc1, 0x00}); err != nil {
		t.Fatal(err)
	}
	reply, err := ca.ReadFrame(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeMessage(reply)
	if err != nil || m.Error == nil || m.Error.Code != CodeParseError {
		t.Fatalf("reply = %+v, %v", m, err)
	}
	// Still alive: a valid request is answered.
	req, _ := NewRequest(1, "echo", map[string]int{"x": 1})
	frame, _ := EncodeMessage(req)
	if err := ca.WriteFrame(ctx, frame); err != nil {
		t.Fatal(err)
	}
	reply, _ = ca.ReadFrame(ctx)
	if m, err := DecodeMessage(reply); err != nil || m.Error != nil {
		t.Fatalf("reply = %+v, %v", m, err)
	}
}

func TestPeerOversizedFrameClosesPeer(t *testing.T) {
	ca, cb := newPipe()
	b := startOne(t, cb)
	if err := ca.WriteFrame(context.Background(), make([]byte, MaxFrameSize+1)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("peer did not close on oversized frame")
	}
	if err := b.Call(context.Background(), "x", nil, nil); !errors.Is(err, ErrClosed) || !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestPeerOversizedWriteRejected(t *testing.T) {
	a, _ := pipePair(t)
	big := strings.Repeat("a", MaxFrameSize)
	if err := a.Notify(context.Background(), "x", big); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if err := a.Call(context.Background(), "x", big, nil); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestWebSocketRejectsOversizedAndText(t *testing.T) {
	srvDone := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		wc := NewWSConn(c)
		for {
			if _, err := wc.ReadFrame(r.Context()); err != nil {
				srvDone <- err
				_ = wc.Close()
				return
			}
		}
	}))
	defer srv.Close()

	tests := []struct {
		name string
		typ  websocket.MessageType
		size int
	}{
		{"oversized", websocket.MessageBinary, MaxFrameSize + 1},
		{"text", websocket.MessageText, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.CloseNow() }()
			_ = c.Write(ctx, tt.typ, make([]byte, tt.size))
			select {
			case err := <-srvDone:
				if err == nil {
					t.Fatal("expected error")
				}
			case <-ctx.Done():
				t.Fatal("server did not reject frame")
			}
		})
	}
	// The write side refuses oversized frames too.
	w := &WSConn{}
	if err := w.WriteFrame(context.Background(), make([]byte, MaxFrameSize+1)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestPeerCallContextCancel(t *testing.T) {
	a, b := pipePair(t)
	release := make(chan struct{})
	b.Handle("slow", func(ctx context.Context, _ Params) (any, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := a.Call(ctx, "slow", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	close(release)
	// A late response for the abandoned call is ignored.
	b.Handle("fast", func(context.Context, Params) (any, error) { return "ok", nil })
	var s string
	if err := a.Call(context.Background(), "fast", nil, &s); err != nil || s != "ok" {
		t.Fatalf("after cancel: %q %v", s, err)
	}
}

func TestPeerCloseUnblocksPendingCalls(t *testing.T) {
	a, b := pipePair(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	b.Handle("hang", func(ctx context.Context, _ Params) (any, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil, nil
	})
	const n = 10
	errs := make(chan error, n)
	for range n {
		go func() { errs <- a.Call(context.Background(), "hang", nil, nil) }()
	}
	time.Sleep(50 * time.Millisecond)
	_ = a.Close()
	_ = a.Close() // idempotent
	for range n {
		select {
		case err := <-errs:
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("err = %v, want ErrClosed", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("pending call not unblocked")
		}
	}
	if err := a.Notify(context.Background(), "x", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Notify after close = %v", err)
	}
}

func TestPeerRemoteDisconnectFailsCalls(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			a, b := tr.pair(t)
			b.Handle("hang", func(ctx context.Context, _ Params) (any, error) {
				<-ctx.Done()
				return nil, nil
			})
			errc := make(chan error, 1)
			go func() { errc <- a.Call(context.Background(), "hang", nil, nil) }()
			time.Sleep(50 * time.Millisecond)
			_ = b.Close()
			select {
			case err := <-errc:
				if !errors.Is(err, ErrClosed) {
					t.Fatalf("err = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("call not failed after remote close")
			}
		})
	}
}

func TestPeerRunReturns(t *testing.T) {
	t.Run("ctx cancel", func(t *testing.T) {
		ca, _ := newPipe()
		p := NewPeer(ca, quiet())
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- p.Run(ctx) }()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
		select {
		case <-p.Done():
		default:
			t.Fatal("peer not closed after Run")
		}
	})
	t.Run("local close", func(t *testing.T) {
		ca, _ := newPipe()
		p := NewPeer(ca, quiet())
		done := make(chan error, 1)
		go func() { done <- p.Run(context.Background()) }()
		_ = p.Close()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v", err)
		}
	})
	t.Run("remote close", func(t *testing.T) {
		ca, cb := newPipe()
		p := NewPeer(ca, quiet())
		done := make(chan error, 1)
		go func() { done <- p.Run(context.Background()) }()
		_ = cb.Close()
		if err := <-done; !errors.Is(err, io.EOF) {
			t.Fatalf("Run = %v", err)
		}
	})
}
