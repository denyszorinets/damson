package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/version"
)

// outcome describes how one connection attempt ended.
type outcome struct {
	// stable is set when the session stayed up for one heartbeat interval
	// after hello; only then does the reconnect backoff restart.
	stable bool
	err    error // why the attempt ended; nil after a requested reconnect
	// fatal is set when retrying cannot help (bad token, pool conflict, ...).
	fatal error
	// reconnect is the jitter to wait after agent.reconnect.
	reconnect *time.Duration
}

func (o outcome) reason() string {
	switch {
	case o.reconnect != nil:
		return "controller asked to reconnect"
	case o.err != nil:
		return o.err.Error()
	}
	return "connection closed"
}

// session is one connection from dial to close.
type session struct {
	a         *Agent
	peer      *protocol.Peer
	reconnect chan time.Duration
}

// session dials, says hello and serves the connection until it ends.
func (a *Agent) session(ctx context.Context, seq uint64) outcome {
	ws, err := a.dial(ctx)
	if err != nil {
		return outcome{err: err, fatal: fatalDial(err)}
	}
	log := a.log.With("session_seq", seq)
	s := &session{a: a, reconnect: make(chan time.Duration, 1)}
	s.peer = protocol.NewPeer(protocol.NewWSConn(ws), log)
	s.peer.Handle(protocol.MethodAgentDrain, s.handleDrain)
	s.peer.Handle(protocol.MethodAgentReconnect, s.handleReconnect)

	rctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { _ = s.peer.Run(rctx) })
	defer func() {
		cancel()
		_ = s.peer.Close()
		wg.Wait()
	}()

	a.draining.Store(false)
	resp, err := s.hello(rctx, seq)
	if err != nil {
		log.Info("agent.hello failed", "error", err)
		return outcome{err: err, fatal: fatalHello(err)}
	}
	a.connected.Store(true)
	interval := a.cfg.HeartbeatInterval
	if resp.HeartbeatIntervalMS > 0 {
		interval = time.Duration(resp.HeartbeatIntervalMS) * time.Millisecond
	}
	log.Info("connected", "protocol", resp.Protocol, "heartbeat_interval", interval.String())
	wg.Go(func() { s.heartbeatLoop(rctx, interval) })
	st := a.cfg.Clock.timer(interval)
	defer st.Stop()
	stable := func() bool {
		select {
		case <-st.C():
			return true
		default:
			return false
		}
	}

	select {
	case d := <-s.reconnect:
		return outcome{stable: stable(), reconnect: &d}
	case <-s.peer.Done():
		select { // the close may follow an agent.reconnect that raced it
		case d := <-s.reconnect:
			return outcome{stable: stable(), reconnect: &d}
		default:
		}
		return outcome{stable: stable(), err: errors.New("connection lost")}
	case <-ctx.Done():
		return outcome{stable: stable(), err: ctx.Err()}
	}
}

// dial opens the WebSocket with the bearer token. The token only ever sits in
// the request header; errors are built from the status, never from headers.
func (a *Agent) dial(ctx context.Context) (*websocket.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, a.cfg.DialTimeout)
	defer cancel()
	ws, resp, err := websocket.Dial(dctx, a.connURL, &websocket.DialOptions{
		HTTPClient:      a.cfg.HTTPClient,
		CompressionMode: websocket.CompressionDisabled,
		HTTPHeader: http.Header{
			"Authorization":     {"Bearer " + a.cfg.Token},
			"X-Damson-Agent-Id": {a.cfg.AgentID},
		},
	})
	if err != nil {
		if resp != nil {
			return nil, &StatusError{Status: resp.StatusCode}
		}
		return nil, fmt.Errorf("dialing controller: %w", err)
	}
	return ws, nil
}

// StatusError is a refused WebSocket upgrade.
type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("controller refused the connection: HTTP %d %s", e.Status, http.StatusText(e.Status))
}

// fatalDial: a bad request, unknown or revoked token, or a forbidden
// transport will not fix itself. Other statuses (503 while a controller
// starts or stops, 502/504 from a proxy) are retried.
func fatalDial(err error) error {
	var se *StatusError
	if !errors.As(err, &se) {
		return nil
	}
	switch se.Status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return err
	}
	return nil
}

// fatalHello: hello errors that only a configuration change can fix.
// CodeStaleSession is not among them: the next attempt uses a higher session_seq.
func fatalHello(err error) error {
	var re *protocol.RPCError
	if !errors.As(err, &re) {
		return nil
	}
	switch re.Code {
	case protocol.CodePoolConflict, protocol.CodeUnsupportedProtocol, protocol.CodeInvalidParams:
		return err
	}
	return nil
}

func (s *session) hello(ctx context.Context, seq uint64) (protocol.HelloResponse, error) {
	cfg := &s.a.cfg
	ctx, cancel := context.WithTimeout(ctx, cfg.HelloTimeout)
	defer cancel()
	req := protocol.HelloRequest{
		Protocol:      protocol.ProtocolVersion,
		SessionSeq:    seq,
		AgentVersion:  versionOr(cfg.Version),
		Pool:          cfg.Pool,
		Labels:        cfg.labels(),
		MaxConcurrent: cfg.Slots,
		Capabilities:  cfg.capabilities(),
		Manifests:     cfg.manifests(),
		Executions:    []protocol.ExecutionState{},
	}
	var resp protocol.HelloResponse
	if err := s.peer.Call(ctx, protocol.MethodAgentHello, req, &resp); err != nil {
		return resp, fmt.Errorf("agent.hello: %w", err)
	}
	return resp, nil
}

func versionOr(v string) string {
	if v == "" {
		return version.Version
	}
	return v
}

// handleDrain stops new work. No work exists before dispatch lands (M2); the
// flag makes heartbeats report no free slots.
func (s *session) handleDrain(_ context.Context, p protocol.Params) (any, error) {
	var d protocol.Drain
	if err := p.Decode(&d); err != nil {
		return nil, err
	}
	s.a.draining.Store(true)
	s.a.log.Info("draining: not accepting new work", "reason", d.Reason)
	return nil, nil
}

// handleReconnect schedules a reconnect after a random delay in [0, jitter].
func (s *session) handleReconnect(_ context.Context, p protocol.Params) (any, error) {
	var rc protocol.Reconnect
	if err := p.Decode(&rc); err != nil {
		return nil, err
	}
	jitter := time.Duration(max(rc.JitterMS, 0)) * time.Millisecond
	d := time.Duration(s.a.cfg.Rand() * float64(jitter))
	s.a.log.Info("controller asked to reconnect", "reason", rc.Reason, "wait", d.String())
	select {
	case s.reconnect <- d:
	default: // one is already pending
	}
	return nil, nil
}

// heartbeatLoop sends a heartbeat now and then every interval until ctx or
// the peer ends.
func (s *session) heartbeatLoop(ctx context.Context, interval time.Duration) {
	t := s.a.cfg.Clock.ticker(interval)
	defer t.Stop()
	for {
		if err := s.heartbeat(ctx); err != nil {
			s.a.log.Debug("heartbeat failed", "error", err)
			return
		}
		select {
		case <-t.C():
		case <-ctx.Done():
			return
		case <-s.peer.Done():
			return
		}
	}
}

func (s *session) heartbeat(ctx context.Context) error {
	cfg := &s.a.cfg
	tel := cfg.Sampler.Sample()
	free := cfg.Slots
	if s.a.draining.Load() {
		free = 0
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.HelloTimeout)
	defer cancel()
	return s.peer.Notify(ctx, protocol.MethodAgentHeartbeat, protocol.Heartbeat{
		RunningAttempts: []string{},
		FreeSlots:       free,
		CPUPercent:      tel.CPUPercent,
		MemoryBytes:     tel.MemoryBytes,
		Load1:           tel.Load1,
	})
}
