package controller

import (
	"context"
	"errors"

	"github.com/denyszorinets/damson/internal/store"
)

// flushLoop writes batched heartbeat data every FlushInterval.
func (g *Gateway) flushLoop(ctx context.Context) {
	t := g.cfg.Clock.ticker(g.cfg.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			g.flush(ctx)
		}
	}
}

// flush writes one BatchTouchAgents for every session with new data. Rows the
// store skips (fenced by session_seq or ownership) are not errors.
func (g *Gateway) flush(ctx context.Context) {
	var touches []store.AgentTouch
	var taken []*session
	for _, s := range g.reg.all() {
		if t, ok := s.takeTouch(); ok {
			touches = append(touches, t)
			taken = append(taken, s)
		}
	}
	if len(touches) == 0 {
		return
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.cfg.StoreTimeout)
	defer cancel()
	n, err := g.cfg.Store.BatchTouchAgents(sctx, g.cfg.ControllerID, touches)
	if err != nil {
		g.log.Error("writing agent telemetry batch", "agents", len(touches), "error", err)
		for _, s := range taken {
			s.redirty()
		}
		return
	}
	g.log.Debug("agent telemetry batch written", "agents", len(touches), "applied", n)
}

// instanceLoop heartbeats controller_instances every InstanceInterval and
// re-registers if the row vanished.
func (g *Gateway) instanceLoop(ctx context.Context) {
	t := g.cfg.Clock.ticker(g.cfg.InstanceInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			g.heartbeatInstance(ctx)
		}
	}
}

func (g *Gateway) heartbeatInstance(ctx context.Context) {
	sctx, cancel := context.WithTimeout(ctx, g.cfg.StoreTimeout)
	defer cancel()
	now := g.cfg.Clock.now()
	err := g.cfg.Store.HeartbeatControllerInstance(sctx, g.cfg.ControllerID, now)
	if errors.Is(err, store.ErrNotFound) {
		err = g.cfg.Store.RegisterControllerInstance(sctx, g.cfg.ControllerID, g.cfg.InternalURL, now)
	}
	if err != nil && ctx.Err() == nil {
		g.log.Error("controller instance heartbeat failed", "controller_id", g.cfg.ControllerID, "error", err)
	}
}

// disconnect releases a finished socket: it leaves the registry and, if it
// still owned the agent row, marks the agent disconnected (CAS on
// controller_id and session_seq). A socket superseded by a newer one does
// nothing: the newer hello already took the row.
func (g *Gateway) disconnect(s *session) {
	seq, ok := s.sessionSeq()
	if !ok {
		s.log.Debug("agent socket closed before hello")
		return
	}
	if !g.reg.remove(s) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(g.base), g.cfg.StoreTimeout)
	defer cancel()
	err := g.cfg.Store.MarkAgentDisconnected(ctx, s.agentID, g.cfg.ControllerID, seq, g.cfg.Clock.now())
	switch {
	case err == nil:
		s.log.Info("agent disconnected", "session_seq", seq)
	case errors.Is(err, store.ErrStale), errors.Is(err, store.ErrNotFound):
		s.log.Debug("agent already owned elsewhere", "session_seq", seq)
	default:
		s.log.Error("marking agent disconnected", "session_seq", seq, "error", err)
	}
}
