package agent

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/denyszorinets/damson/internal/protocol"
)

// fakeCtl is a scripted controller: it accepts agent connections, records
// everything the agent sends (decoded, and as raw frames for secret scans)
// and answers hello and step.complete through hooks.
type fakeCtl struct {
	t      *testing.T
	ctx    context.Context
	srv    *httptest.Server
	wg     sync.WaitGroup
	closed atomic.Bool

	// Set before the agent starts.
	helloResp  func(n int, req protocol.HelloRequest) protocol.HelloResponse
	onComplete func(n int, c *fakeConn, sc protocol.StepComplete) (protocol.StepCompleteAck, error)
	autoAck    atomic.Bool // answer each log.append with log.ack

	mu        sync.Mutex
	conns     []*fakeConn
	hellos    []protocol.HelloRequest
	logs      []protocol.LogAppend
	completes []protocol.StepComplete
	hostkeys  []protocol.HostKeyObserved
	progress  []protocol.StepProgress
	beats     []protocol.Heartbeat
	frames    [][]byte // raw frames received from the agent
}

type fakeConn struct {
	ctl  *fakeCtl
	peer *protocol.Peer
}

// recConn records the frames the agent sends.
type recConn struct {
	protocol.Conn
	ctl *fakeCtl
}

func (r recConn) ReadFrame(ctx context.Context) ([]byte, error) {
	b, err := r.Conn.ReadFrame(ctx)
	if err == nil {
		r.ctl.mu.Lock()
		r.ctl.frames = append(r.ctl.frames, append([]byte(nil), b...))
		r.ctl.mu.Unlock()
	}
	return b, err
}

func newFakeCtl(t *testing.T) *fakeCtl {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := &fakeCtl{t: t, ctx: ctx}
	c.helloResp = func(int, protocol.HelloRequest) protocol.HelloResponse {
		return protocol.HelloResponse{Protocol: protocol.ProtocolVersion, HeartbeatIntervalMS: 20}
	}
	c.srv = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(func() {
		c.closed.Store(true)
		cancel()
		c.mu.Lock()
		for _, fc := range c.conns {
			_ = fc.peer.Close()
		}
		c.mu.Unlock()
		c.srv.Close()
		c.wg.Wait()
	})
	return c
}

func (c *fakeCtl) url() string { return "ws" + strings.TrimPrefix(c.srv.URL, "http") }

func (c *fakeCtl) serve(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	fc := &fakeConn{ctl: c}
	fc.peer = protocol.NewPeer(recConn{protocol.NewWSConn(ws), c}, slog.New(slog.DiscardHandler))
	c.mu.Lock()
	if c.closed.Load() {
		c.mu.Unlock()
		_ = fc.peer.Close()
		return
	}
	c.conns = append(c.conns, fc)
	n := len(c.conns)
	c.wg.Add(1)
	c.mu.Unlock()
	defer c.wg.Done()
	fc.handlers(n)
	_ = fc.peer.Run(c.ctx)
	_ = fc.peer.Close()
}

func (fc *fakeConn) handlers(n int) {
	c := fc.ctl
	fc.peer.Handle(protocol.MethodAgentHello, func(_ context.Context, p protocol.Params) (any, error) {
		var req protocol.HelloRequest
		if err := p.Decode(&req); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.hellos = append(c.hellos, req)
		c.mu.Unlock()
		return c.helloResp(n, req), nil
	})
	fc.peer.Handle(protocol.MethodAgentHeartbeat, func(_ context.Context, p protocol.Params) (any, error) {
		var hb protocol.Heartbeat
		if err := p.Decode(&hb); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.beats = append(c.beats, hb)
		c.mu.Unlock()
		return nil, nil
	})
	fc.peer.Handle(protocol.MethodLogAppend, func(ctx context.Context, p protocol.Params) (any, error) {
		var la protocol.LogAppend
		if err := p.Decode(&la); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.logs = append(c.logs, la)
		c.mu.Unlock()
		if c.autoAck.Load() && len(la.Entries) > 0 {
			_ = fc.peer.Notify(ctx, protocol.MethodLogAck, protocol.LogAck{AttemptID: la.AttemptID, Seq: la.SeqFrom + uint64(len(la.Entries)) - 1})
		}
		return nil, nil
	})
	fc.peer.Handle(protocol.MethodStepProgress, func(_ context.Context, p protocol.Params) (any, error) {
		var sp protocol.StepProgress
		if err := p.Decode(&sp); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.progress = append(c.progress, sp)
		c.mu.Unlock()
		return nil, nil
	})
	fc.peer.Handle(protocol.MethodNodeHostKeyObserved, func(_ context.Context, p protocol.Params) (any, error) {
		var hk protocol.HostKeyObserved
		if err := p.Decode(&hk); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.hostkeys = append(c.hostkeys, hk)
		c.mu.Unlock()
		return nil, nil
	})
	fc.peer.Handle(protocol.MethodStepComplete, func(_ context.Context, p protocol.Params) (any, error) {
		var sc protocol.StepComplete
		if err := p.Decode(&sc); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.completes = append(c.completes, sc)
		c.mu.Unlock()
		if c.onComplete == nil {
			return protocol.StepCompleteAck{Acknowledged: true}, nil
		}
		return c.onComplete(n, fc, sc)
	})
}

// --- accessors ---

func (c *fakeCtl) latest() *fakeConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.conns) == 0 {
		return nil
	}
	return c.conns[len(c.conns)-1]
}

func (c *fakeCtl) helloList() []protocol.HelloRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]protocol.HelloRequest(nil), c.hellos...)
}

func (c *fakeCtl) completeList() []protocol.StepComplete {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]protocol.StepComplete(nil), c.completes...)
}

func (c *fakeCtl) hostKeyList() []protocol.HostKeyObserved {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]protocol.HostKeyObserved(nil), c.hostkeys...)
}

func (c *fakeCtl) progressList() []protocol.StepProgress {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]protocol.StepProgress(nil), c.progress...)
}

func (c *fakeCtl) lastBeat() (protocol.Heartbeat, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.beats) == 0 {
		return protocol.Heartbeat{}, false
	}
	return c.beats[len(c.beats)-1], true
}

// logsOf returns the log.append messages for attempt in arrival order.
func (c *fakeCtl) logsOf(attempt string) []protocol.LogAppend {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []protocol.LogAppend
	for _, l := range c.logs {
		if l.AttemptID == attempt {
			out = append(out, l)
		}
	}
	return out
}

// lines flattens the received entries of attempt into seq order, de-duplicated
// (a reconnect resends unacknowledged batches).
func (c *fakeCtl) lines(attempt string) map[uint64]protocol.LogEntry {
	out := map[uint64]protocol.LogEntry{}
	for _, la := range c.logsOf(attempt) {
		for i, e := range la.Entries {
			out[la.SeqFrom+uint64(i)] = e
		}
	}
	return out
}

func (c *fakeCtl) rawFrames() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.frames...)
}

// --- commands to the agent ---

func (c *fakeCtl) execute(req protocol.StepExecute) (protocol.StepExecuteResult, error) {
	c.t.Helper()
	var res protocol.StepExecuteResult
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	err := c.latest().peer.Call(ctx, protocol.MethodStepExecute, req, &res)
	return res, err
}

func (c *fakeCtl) mustExecute(req protocol.StepExecute) {
	c.t.Helper()
	res, err := c.execute(req)
	if err != nil || !res.Accepted {
		c.t.Fatalf("step.execute %s: accepted=%v reason=%q err=%v", req.AttemptID, res.Accepted, res.Reason, err)
	}
}

func (c *fakeCtl) cancel(attempt string, graceMS int64) protocol.StepCancelResult {
	c.t.Helper()
	var res protocol.StepCancelResult
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	err := c.latest().peer.Call(ctx, protocol.MethodStepCancel, protocol.StepCancel{AttemptID: attempt, GraceMS: graceMS}, &res)
	if err != nil {
		c.t.Fatalf("step.cancel: %v", err)
	}
	return res
}

func (c *fakeCtl) notify(method string, params any) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	if err := c.latest().peer.Notify(ctx, method, params); err != nil {
		c.t.Fatalf("notify %s: %v", method, err)
	}
}
