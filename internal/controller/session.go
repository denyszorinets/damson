package controller

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

// maxReportedAttempts bounds running_attempts kept in the stored telemetry.
const maxReportedAttempts = 1024

// session is one agent socket. Its fields after hello are guarded by mu.
type session struct {
	agentID string
	pool    store.Pool
	peer    *protocol.Peer
	log     *slog.Logger

	mu        sync.Mutex
	helloing  bool // a hello is in progress or done on this socket
	helloed   bool // a hello succeeded; seq is valid
	seq       uint64
	telemetry json.RawMessage
	lastSeen  time.Time
	dirty     bool
	// free and running are the dispatcher's view: soft state from the hello
	// and the latest heartbeat, minus slots reserved since. The agent's
	// "no free slot" rejection is the authority, so drift only costs a requeue.
	free    int
	running []string
}

func (g *Gateway) newSession(id identity, conn protocol.Conn) *session {
	log := g.log.With("agent_id", id.agentID, "pool_id", id.pool.ID)
	s := &session{agentID: id.agentID, pool: id.pool, log: log}
	s.peer = protocol.NewPeer(conn, log)
	for _, h := range g.hooks {
		h.Register(s.peer, id.agentID)
	}
	s.peer.Handle(protocol.MethodAgentHello, func(ctx context.Context, p protocol.Params) (any, error) {
		return g.handleHello(ctx, s, p)
	})
	s.peer.Handle(protocol.MethodAgentHeartbeat, func(_ context.Context, p protocol.Params) (any, error) {
		return nil, g.handleHeartbeat(s, p)
	})
	return s
}

func (s *session) isHelloed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.helloed
}

// recordHeartbeat keeps the latest snapshot for the next batch write.
func (s *session) recordHeartbeat(hb protocol.Heartbeat, at time.Time) error {
	if len(hb.RunningAttempts) > maxReportedAttempts {
		hb.RunningAttempts = hb.RunningAttempts[:maxReportedAttempts]
	}
	raw, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.helloed {
		return nil
	}
	s.telemetry, s.lastSeen, s.dirty = raw, at, true
	s.free, s.running = max(hb.FreeSlots, 0), hb.RunningAttempts
	return nil
}

// reserve takes up to n free slots and returns how many it took.
func (s *session) reserve(n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n = max(min(n, s.free), 0)
	s.free -= n
	return n
}

// release returns n slots, for a claim that was not sent.
func (s *session) release(n int) {
	s.mu.Lock()
	s.free += n
	s.mu.Unlock()
}

func (s *session) dispatchView() AgentView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return AgentView{ID: s.agentID, Pool: s.pool.Name, Free: s.free, Running: append([]string(nil), s.running...), Peer: s.peer}
}

// takeTouch returns the pending batch row and clears the dirty flag.
func (s *session) takeTouch() (store.AgentTouch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return store.AgentTouch{}, false
	}
	s.dirty = false
	return store.AgentTouch{ID: s.agentID, SessionSeq: s.seq, LastSeen: s.lastSeen, Telemetry: s.telemetry}, true
}

// redirty makes the next flush retry after a failed batch write.
func (s *session) redirty() {
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
}

func (s *session) seqValue() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

func (s *session) sessionSeq() (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq, s.helloed
}
