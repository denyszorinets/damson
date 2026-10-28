package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/protocol"
)

// Gateway is the controller's agent WebSocket endpoint. Create it with New,
// mount Handler at ConnectPath, then Start it. Stop sends agent.reconnect to
// every socket and waits for them to finish.
type Gateway struct {
	cfg Config
	log *slog.Logger
	reg *registry

	mu       sync.Mutex
	started  bool
	stopping bool
	handlers sync.WaitGroup        // upgraded connections
	open     map[*session]struct{} // every upgraded socket, helloed or not

	base   context.Context // lives until the end of Stop; owns the sockets
	cancel context.CancelFunc
	loops  context.CancelFunc // background loops (flush, instance heartbeat)
	loopWG sync.WaitGroup
}

// New validates cfg. Nothing runs until Start.
func New(cfg Config) (*Gateway, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	return &Gateway{cfg: cfg, log: cfg.Logger, reg: newRegistry(), open: make(map[*session]struct{})}, nil
}

// Handler serves ConnectPath.
func (g *Gateway) Handler() http.Handler { return http.HandlerFunc(g.serve) }

// Connected returns the ids of the agents with a live, helloed socket.
func (g *Gateway) Connected() []string { return g.reg.ids() }

// Peer returns the protocol peer of a connected agent, for dispatch.
func (g *Gateway) Peer(agentID string) (*protocol.Peer, bool) {
	s := g.reg.get(agentID)
	if s == nil {
		return nil, false
	}
	return s.peer, true
}

// Start registers the controller instance and launches the background loops.
// The context supplies values only: sockets and loops end through Stop, never
// through cancellation of ctx, so a signal-cancelled ctx cannot cut the
// graceful agent.reconnect short.
func (g *Gateway) Start(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.started {
		return errors.New("controller: gateway already started")
	}
	if err := g.cfg.Store.RegisterControllerInstance(ctx, g.cfg.ControllerID, g.cfg.InternalURL, g.cfg.Clock.now()); err != nil {
		return fmt.Errorf("registering controller instance: %w", err)
	}
	g.base, g.cancel = context.WithCancel(context.WithoutCancel(ctx))
	var loopCtx context.Context
	loopCtx, g.loops = context.WithCancel(g.base)
	g.started = true
	g.loopWG.Add(2)
	go func() { defer g.loopWG.Done(); g.instanceLoop(loopCtx) }()
	go func() { defer g.loopWG.Done(); g.flushLoop(loopCtx) }()
	g.log.Info("agent gateway started", "controller_id", g.cfg.ControllerID, "insecure_dev", g.cfg.InsecureDev)
	return nil
}

// Stop refuses new connections, tells every agent to reconnect elsewhere,
// writes the last telemetry batch, closes the sockets, waits for them until
// ctx expires and deregisters the controller instance. It is safe to call
// without Start and more than once.
func (g *Gateway) Stop(ctx context.Context) error {
	g.mu.Lock()
	if !g.started || g.stopping {
		g.mu.Unlock()
		return nil
	}
	g.stopping = true
	g.mu.Unlock()

	g.loops()
	g.loopWG.Wait()
	g.sendReconnect(ctx)
	g.flush(ctx)
	g.mu.Lock()
	for s := range g.open {
		_ = s.peer.Close()
	}
	g.mu.Unlock()
	waitErr := g.waitHandlers(ctx)
	g.cancel()
	delErr := g.cfg.Store.DeleteControllerInstance(ctx, g.cfg.ControllerID)
	if delErr != nil {
		delErr = fmt.Errorf("deregistering controller instance: %w", delErr)
	}
	g.log.Info("agent gateway stopped", "controller_id", g.cfg.ControllerID)
	return errors.Join(waitErr, delErr)
}

func (g *Gateway) waitHandlers(ctx context.Context) error {
	done := make(chan struct{})
	go func() { g.handlers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		g.cancel() // force the stragglers: their Run returns on cancellation
		<-done
		return fmt.Errorf("waiting for agent sockets: %w", ctx.Err())
	}
}

// sendReconnect notifies every live socket in parallel; one stuck socket
// delays only itself, bounded by StoreTimeout.
func (g *Gateway) sendReconnect(ctx context.Context) {
	msg := protocol.Reconnect{JitterMS: g.cfg.ReconnectJitter.Milliseconds(), Reason: "controller shutting down"}
	var wg sync.WaitGroup
	for _, s := range g.reg.all() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, g.cfg.StoreTimeout)
			defer cancel()
			if err := s.peer.Notify(sctx, protocol.MethodAgentReconnect, msg); err != nil {
				s.log.Debug("sending agent.reconnect failed", "error", err)
			}
		}()
	}
	wg.Wait()
}

// admit reserves a handler slot, or reports that the gateway is not accepting.
func (g *Gateway) admit() (context.Context, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.started || g.stopping {
		return nil, false
	}
	g.handlers.Add(1)
	return g.base, true
}

// serve authenticates, upgrades and then runs the agent session until the
// socket ends.
func (g *Gateway) serve(w http.ResponseWriter, r *http.Request) {
	if !g.accepting() {
		api.WriteProblem(w, r, http.StatusServiceUnavailable, "urn:damson:problem:unavailable", "the controller is not accepting agents")
		return
	}
	id, ok := g.authenticate(w, r)
	if !ok {
		return
	}
	base, ok := g.admit()
	if !ok {
		api.WriteProblem(w, r, http.StatusServiceUnavailable, "urn:damson:problem:unavailable", "the controller is not accepting agents")
		return
	}
	defer g.handlers.Done()

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		g.log.Warn("websocket upgrade failed", "agent_id", id.agentID, "error", err)
		return
	}
	g.touchToken(r.Context(), id)
	s := g.newSession(id, protocol.NewWSConn(ws))
	g.track(s, true)
	defer g.track(s, false)
	s.log.Info("agent connected", "pool", id.pool.Name)
	timer := time.AfterFunc(g.cfg.HelloTimeout, func() {
		if !s.isHelloed() {
			s.log.Warn("closing socket: no agent.hello in time")
			_ = s.peer.Close()
		}
	})
	defer timer.Stop()

	if err := s.peer.Run(base); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Debug("agent socket ended", "error", err)
	}
	g.disconnect(s)
}

func (g *Gateway) touchToken(ctx context.Context, id identity) {
	if err := g.cfg.Store.TouchPoolToken(ctx, id.tokenID, g.cfg.Clock.now()); err != nil {
		g.log.Warn("recording pool token use", "token_id", id.tokenID, "error", err)
	}
}

func (g *Gateway) track(s *session, add bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if add {
		g.open[s] = struct{}{}
		if g.stopping {
			_ = s.peer.Close() // Stop already swept; do not outlive it
		}
		return
	}
	delete(g.open, s)
}

func (g *Gateway) accepting() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.started && !g.stopping
}
