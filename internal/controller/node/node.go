// Package node composes one controller process: the agent gateway, run.advance
// workers, log ingest, the dispatcher and the HTTP API, with the production
// hook order, start order and stop order (ARCHITECTURE section 19). The
// damson binary and the HA chaos test both run this composition.
package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/controller"
	"github.com/denyszorinets/damson/internal/controller/advance"
	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/controller/logs"
	"github.com/denyszorinets/damson/internal/devnodes"
	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

// Timing overrides the background intervals. Zero values select each
// component's default; tests shorten them.
type Timing struct {
	Heartbeat, Flush, PluginSync  time.Duration // gateway
	ReconnectJitter               time.Duration // gateway: bound of the agent.reconnect delay
	Dispatch, Renew, Reap, Cancel time.Duration // dispatcher
	IncapableRetry                time.Duration // dispatcher: delay of an execution an agent cannot run
	LeaseTTL                      time.Duration // dispatcher and adoption on reconnect
	AdvancePoll, AdvanceSweep     time.Duration // run.advance
}

// Config assembles a controller.
type Config struct {
	Store        store.Store
	ControllerID string
	InternalURL  string
	Plugins      *pluginreg.Registry
	// FinallyGrace is how long finally blocks may run after a cancel or the
	// job timeout; zero selects the engine default. All controllers of a
	// cluster must use the same value.
	FinallyGrace time.Duration
	// DevNodes replaces inventory and secret authorization (development
	// only, ADR-0034). Nil in production.
	DevNodes *devnodes.File

	InsecureDev         bool
	TrustForwardedProto bool

	// Addr and UI configure the HTTP server returned by Server.
	Addr string
	UI   http.Handler

	Timing Timing
	Logger *slog.Logger
}

// Node is a running controller.
type Node struct {
	gw   *controller.Gateway
	adv  *advance.Advancer
	logs *logs.Ingest
	disp *dispatch.Dispatcher
	srv  *api.Server
}

// Start builds the components on the store and starts the run machinery. On
// an unmigrated database (version 0) the gateway and run.advance stay off and
// the API keeps answering health checks; the operator migrates and restarts.
// Nothing listens until Listen is called.
func Start(ctx context.Context, cfg Config) (*Node, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	t := cfg.Timing
	gw, err := controller.New(controller.Config{
		Store: cfg.Store, Plugins: cfg.Plugins, ControllerID: cfg.ControllerID, InternalURL: cfg.InternalURL,
		InsecureDev: cfg.InsecureDev, TrustForwardedProto: cfg.TrustForwardedProto, Logger: cfg.Logger,
		HeartbeatInterval: t.Heartbeat, FlushInterval: t.Flush, PluginSyncInterval: t.PluginSync, LeaseTTL: t.LeaseTTL, ReconnectJitter: t.ReconnectJitter,
	})
	if err != nil {
		return nil, fmt.Errorf("creating agent gateway: %w", err)
	}
	n := &Node{gw: gw}
	if v, err := cfg.Store.MigrationVersion(ctx); err != nil {
		return nil, fmt.Errorf("reading migration version: %w", err)
	} else if v == 0 {
		cfg.Logger.Warn("database is not migrated; agent gateway and run.advance disabled (run with --auto-migrate)")
	} else if err := n.startOrchestration(ctx, cfg); err != nil {
		return nil, err
	}
	apiCfg := api.Config{
		Addr: cfg.Addr, Store: cfg.Store, Data: cfg.Store, Plugins: cfg.Plugins, UI: cfg.UI, Logger: cfg.Logger,
		Mounts: map[string]http.Handler{controller.ConnectPath: gw.Handler()},
	}
	if n.adv != nil {
		apiCfg.Advance, apiCfg.RunNotify = n.adv, n.adv.Hub()
	}
	n.srv = api.NewServer(apiCfg)
	return n, nil
}

// startOrchestration builds the components, hooks log ingest and step.complete
// into the gateway and starts everything. Agents are accepted last, once every
// handler exists.
func (n *Node) startOrchestration(ctx context.Context, cfg Config) error {
	log, t := cfg.Logger, cfg.Timing
	eng, err := engine.New(expr.Limits{}, engine.WithFinallyGrace(cfg.FinallyGrace))
	if err != nil {
		return fmt.Errorf("building engine: %w", err)
	}
	if n.adv, err = advance.New(advance.Config{
		Store: cfg.Store, Engine: eng, Logger: log, PollInterval: t.AdvancePoll, SweepInterval: t.AdvanceSweep,
	}); err != nil {
		return fmt.Errorf("creating run advancer: %w", err)
	}
	gw := n.gw
	if n.logs, err = logs.New(logs.Config{Store: cfg.Store, Logger: log, Acks: func(ctx context.Context, agentID string, ack protocol.LogAck) error {
		peer, ok := gw.Peer(agentID)
		if !ok {
			return errors.New("agent not connected")
		}
		return peer.Notify(ctx, protocol.MethodLogAck, ack)
	}}); err != nil {
		return fmt.Errorf("creating log ingest: %w", err)
	}
	dcfg := dispatch.Config{
		Store: cfg.Store, Agents: gw, Plugins: cfg.Plugins, Advance: n.adv, ControllerID: cfg.ControllerID, Logger: log,
		Invalidate: n.logs.Invalidate, Finish: n.logs.Finish,
		DispatchInterval: t.Dispatch, RenewInterval: t.Renew, ReapInterval: t.Reap, CancelInterval: t.Cancel, IncapableRetry: t.IncapableRetry, LeaseTTL: t.LeaseTTL,
	}
	if cfg.DevNodes != nil { // a nil *File must not become a non-nil interface
		dcfg.Nodes, dcfg.Secrets = cfg.DevNodes, cfg.DevNodes
	}
	if n.disp, err = dispatch.New(dcfg); err != nil {
		return fmt.Errorf("creating dispatcher: %w", err)
	}
	for _, h := range []controller.SessionHook{n.logs, n.disp} {
		if err := gw.AddHook(h); err != nil {
			return err
		}
	}
	// Each step is started in order; on failure the ones already running stop.
	for _, step := range []struct {
		name  string
		start func(context.Context) error
	}{{"run.advance", n.adv.Start}, {"log ingest", n.logs.Start}, {"dispatcher", n.disp.Start}, {"agent gateway", gw.Start}} {
		if err := step.start(ctx); err != nil {
			return errors.Join(fmt.Errorf("starting %s: %w", step.name, err), n.stopOrchestration(context.WithoutCancel(ctx)))
		}
	}
	return nil
}

// Server is the HTTP server (API, UI and the agent connect path).
func (n *Node) Server() *api.Server { return n.srv }

// Handler serves the same routes without a listener, for tests.
func (n *Node) Handler() http.Handler { return n.srv.Handler() }

// Listen binds the configured address and serves in the background.
func (n *Node) Listen(ctx context.Context) error { return n.srv.Start(ctx) }

// Advancer returns the run.advance component, or nil on an unmigrated database.
func (n *Node) Advancer() *advance.Advancer { return n.adv }

// Stop closes the listener, then shuts the run machinery down in reverse
// dependency order: no new dispatches, agents told to reconnect, buffered logs
// flushed, then the run.advance workers drain. Stopping a component that never
// started is a no-op.
func (n *Node) Stop(ctx context.Context) error {
	return errors.Join(n.srv.Stop(ctx), n.stopOrchestration(ctx))
}

func (n *Node) stopOrchestration(ctx context.Context) error {
	if n.disp == nil { // unmigrated, or construction failed part-way
		return n.gw.Stop(ctx)
	}
	return errors.Join(n.disp.Stop(ctx), n.gw.Stop(ctx), n.logs.Stop(ctx), n.adv.Stop(ctx))
}
