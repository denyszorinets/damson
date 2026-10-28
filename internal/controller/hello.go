package controller

import (
	"context"
	"errors"
	"time"

	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// rejectGrace lets the error response of a refused hello reach the agent
// before the socket is closed.
const rejectGrace = 200 * time.Millisecond

func rpcErr(code int, msg string) *protocol.RPCError {
	return &protocol.RPCError{Code: code, Message: msg}
}

// reject closes the socket shortly after the handler's error is sent.
func (s *session) reject() {
	time.AfterFunc(rejectGrace, func() { _ = s.peer.Close() })
}

// handleHello validates agent.hello, takes ownership of the agent row in the
// database (ordered by session_seq), registers the socket and answers with
// the negotiated version, heartbeat interval and limits.
func (g *Gateway) handleHello(ctx context.Context, s *session, p protocol.Params) (any, error) {
	var req protocol.HelloRequest
	if err := p.Decode(&req); err != nil {
		s.reject()
		return nil, err
	}
	hello, rerr := g.checkHello(s, req)
	if rerr != nil {
		s.log.Warn("agent.hello rejected", "reason", rerr.Message)
		s.reject()
		return nil, rerr
	}
	if g.isStopping() {
		s.reject()
		return nil, rpcErr(protocol.CodeServerOverloaded, "controller is shutting down; reconnect")
	}
	s.mu.Lock()
	if s.helloing {
		s.mu.Unlock()
		return nil, rpcErr(protocol.CodeInvalidRequest, "agent.hello already received on this connection")
	}
	s.helloing = true
	s.mu.Unlock()

	now := g.cfg.Clock.now()
	hello.ControllerID = g.cfg.ControllerID
	if err := g.cfg.Store.UpsertAgentOnHello(ctx, hello, now); err != nil {
		s.mu.Lock()
		s.helloing = false // a refused hello may be retried; the socket is closed anyway
		s.mu.Unlock()
		return nil, g.helloStoreError(s, err)
	}
	s.mu.Lock()
	s.helloed, s.seq, s.lastSeen = true, req.SessionSeq, now
	s.mu.Unlock()
	if loser := g.reg.replace(s, func() { g.setPlugins(s, req) }); loser != nil {
		loser.log.Info("superseded by a newer socket for the same agent", "session_seq", req.SessionSeq)
		if loser == s {
			s.reject()
			return nil, rpcErr(protocol.CodeStaleSession, "stale session_seq: a newer session owns this agent")
		}
		_ = loser.peer.Close()
	}
	s.log.Info("agent hello", "session_seq", req.SessionSeq, "protocol", hello.Protocol,
		"agent_version", req.AgentVersion, "running_attempts", len(req.Executions))
	return protocol.HelloResponse{
		Protocol:            hello.Protocol,
		HeartbeatIntervalMS: int(g.cfg.HeartbeatInterval.Milliseconds()),
		Limits:              limits(),
		Reconciliation:      reconcile(req.Executions),
	}, nil
}

// checkHello validates the request and builds the store row.
func (g *Gateway) checkHello(s *session, req protocol.HelloRequest) (store.AgentHello, *protocol.RPCError) {
	version, err := protocol.Negotiate(req.Protocol)
	switch {
	case err != nil:
		return store.AgentHello{}, rpcErr(protocol.CodeUnsupportedProtocol, err.Error())
	case req.SessionSeq == 0:
		return store.AgentHello{}, rpcErr(protocol.CodeInvalidParams, "session_seq must be at least 1")
	case req.MaxConcurrent < 1:
		return store.AgentHello{}, rpcErr(protocol.CodeInvalidParams, "max_concurrent must be at least 1")
	case req.Pool != "" && req.Pool != s.pool.Name:
		return store.AgentHello{}, rpcErr(protocol.CodeInvalidParams, "pool does not match the pool token")
	}
	caps := make(map[string]string, len(req.Manifests)+len(req.Capabilities))
	for _, c := range req.Capabilities {
		caps[c] = ""
	}
	for _, m := range req.Manifests {
		caps[m.Name] = m.Version
	}
	return store.AgentHello{
		ID: s.agentID, PoolID: s.pool.ID, Version: req.AgentVersion, Protocol: version,
		Labels: req.Labels, Capabilities: caps, Capacity: req.MaxConcurrent, SessionSeq: req.SessionSeq,
	}, nil
}

// helloStoreError maps a store failure to what the agent sees. Detail stays
// in the log.
func (g *Gateway) helloStoreError(s *session, err error) error {
	switch {
	case errors.Is(err, store.ErrStale):
		s.log.Info("agent.hello rejected: stale session_seq")
		s.reject()
		return rpcErr(protocol.CodeStaleSession, "stale session_seq: a newer session owns this agent")
	case errors.Is(err, store.ErrConflict):
		s.log.Warn("agent.hello rejected: agent id belongs to another pool")
		s.reject()
		return rpcErr(protocol.CodePoolConflict, "agent id is registered in another pool")
	default:
		return err // generic internal error for the agent; logged by the peer
	}
}

// reconcile decides the fate of attempts the agent still knows about. No
// executions exist before M2 dispatch lands, so every reported attempt is
// unknown to the controller and is told to cancel (fencing: not current).
func reconcile(execs []protocol.ExecutionState) []protocol.Reconciliation {
	if len(execs) == 0 {
		return nil
	}
	out := make([]protocol.Reconciliation, len(execs))
	for i, e := range execs {
		out[i] = protocol.Reconciliation{AttemptID: e.AttemptID, Action: protocol.ActionCancel}
	}
	return out
}

// handleHeartbeat records the snapshot in memory; the database write is
// batched by flushLoop. It must not block (notification handler).
func (g *Gateway) handleHeartbeat(s *session, p protocol.Params) error {
	var hb protocol.Heartbeat
	if err := p.Decode(&hb); err != nil {
		return err
	}
	return s.recordHeartbeat(hb, g.cfg.Clock.now())
}

func (g *Gateway) isStopping() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopping
}

// setPlugins publishes the agent's manifests to the plugin registry. A
// rejected manifest is logged and skipped; the agent stays connected.
func (g *Gateway) setPlugins(s *session, req protocol.HelloRequest) {
	if g.cfg.Plugins == nil {
		return
	}
	ms := make([]pluginsdk.Manifest, len(req.Manifests))
	for i, m := range req.Manifests {
		ms[i] = m.Manifest
	}
	err := g.cfg.Plugins.SetAgent(pluginreg.AgentInfo{ID: s.agentID, Pool: s.pool.Name, Capabilities: req.Capabilities, Manifests: ms})
	if err != nil {
		s.log.Warn("plugin manifests rejected", "error", err)
	}
}
