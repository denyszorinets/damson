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
	"net"
	"net/http"
	"time"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/controller"
	"github.com/denyszorinets/damson/internal/controller/advance"
	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/controller/internalapi"
	"github.com/denyszorinets/damson/internal/controller/internalauth"
	"github.com/denyszorinets/damson/internal/controller/leader"
	"github.com/denyszorinets/damson/internal/controller/logs"
	"github.com/denyszorinets/damson/internal/controller/tailproxy"
	"github.com/denyszorinets/damson/internal/devnodes"
	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/protocol"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
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
	LeaderTTL, LeaderRenew        time.Duration // leader lease of the singleton duties
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
	// DevFile is the --dev-nodes file (development only, ADR-0044). Its nodes
	// and secrets are seeded into the inventory and the secret store at start;
	// from then on they are ordinary rows. Nil in production.
	DevFile *devnodes.File
	// KEK and KEKPrevious are the base64 key encryption keys of the db secret
	// backend (ARCHITECTURE 9.2). Without a KEK secrets are disabled, unless
	// the database holds encrypted secrets, which refuses the start.
	KEK, KEKPrevious string
	// ClusterSecret enables the internal listener and the proxying of live log
	// tails between controllers (ADR-0040); ClusterSecretPrevious is also
	// accepted when verifying, for rotation. Both are secrets. Without a secret
	// a tail is served only by the controller that owns the execution.
	ClusterSecret, ClusterSecretPrevious string
	// InternalListen is the address of the internal listener. InternalListener,
	// if set, is used instead (tests bind port 0 first to learn the address
	// they register as InternalURL).
	InternalListen   string
	InternalListener net.Listener
	// SingleController makes the node refuse to start while another controller
	// holds the single-controller lease of this database, for backends that
	// support only one controller (SQLite). Misconfiguration guard, not a fence.
	SingleController bool

	InsecureDev         bool
	TrustForwardedProto bool

	// Stream tunes the API's server-sent event endpoints (tests shorten the
	// intervals); zero fields take the defaults.
	Stream api.StreamConfig

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
	lead *leader.Elector
	only *leader.Elector // single-controller guard, nil unless SingleController
	tail api.LogTail
	prox *tailproxy.Tail
	intl *internalapi.Server
	intL net.Listener
	srv  *api.Server
	// secrets is the db secret backend, nil while secrets are disabled.
	secrets *secretsdb.Backend
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
		if cfg.DevFile != nil {
			return nil, errors.New("--dev-nodes needs a migrated database (run with --auto-migrate)")
		}
		cfg.Logger.Warn("database is not migrated; agent gateway and run.advance disabled (run with --auto-migrate)")
	} else if err := n.startSecrets(ctx, cfg); err != nil {
		return nil, err
	} else if err := n.startOrchestration(ctx, cfg); err != nil {
		return nil, errors.Join(err, n.stopGuard(context.WithoutCancel(ctx)))
	}
	apiCfg := api.Config{
		Addr: cfg.Addr, Store: cfg.Store, Data: cfg.Store, Plugins: cfg.Plugins, UI: cfg.UI, Logger: cfg.Logger,
		Stream: cfg.Stream,
		Mounts: map[string]http.Handler{controller.ConnectPath: gw.Handler()},
	}
	if n.adv != nil {
		apiCfg.Advance, apiCfg.RunNotify = n.adv, n.adv.Hub()
		apiCfg.LogTail = n.tail
	}
	apiCfg.Targets = &InventoryTargets{Store: cfg.Store, Scope: inventory.AllNodes{}}
	apiCfg.AuthorizeSecretRefs = n.secrets != nil
	if n.secrets != nil { // a nil *Backend must not become a non-nil interface
		apiCfg.Secrets = n.secrets
	}
	n.srv = api.NewServer(apiCfg)
	return n, nil
}

// startOrchestration builds the components, hooks log ingest and step.complete
// into the gateway and starts everything. Agents are accepted last, once every
// handler exists.
func (n *Node) startOrchestration(ctx context.Context, cfg Config) error {
	log, t := cfg.Logger, cfg.Timing
	if cfg.SingleController {
		if err := n.guardSingleController(ctx, cfg); err != nil {
			return err
		}
	}
	// Seed only once this process is known to be the one using the database: a
	// second process on an SQLite file must not change the live inventory before
	// it is refused.
	if err := n.seedDevFile(ctx, cfg); err != nil {
		return err
	}
	eng, err := engine.New(expr.Limits{}, engine.WithFinallyGrace(cfg.FinallyGrace))
	if err != nil {
		return fmt.Errorf("building engine: %w", err)
	}
	acfg := advance.Config{
		Store: cfg.Store, Engine: eng, Logger: log, PollInterval: t.AdvancePoll, SweepInterval: t.AdvanceSweep,
	}
	if n.adv, err = advance.New(acfg); err != nil {
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
	if n.lead, err = leader.New(leader.Config{
		Store: cfg.Store, Name: dispatch.LeaderReaper, Holder: cfg.ControllerID, TTL: t.LeaderTTL, Renew: t.LeaderRenew, Logger: log,
	}); err != nil {
		return fmt.Errorf("creating leader elector: %w", err)
	}
	dcfg := dispatch.Config{
		Store: cfg.Store, Agents: gw, Plugins: cfg.Plugins, Advance: n.adv, ControllerID: cfg.ControllerID, Logger: log,
		Leader: n.lead, Invalidate: n.logs.Invalidate, Finish: n.logs.Finish,
		DispatchInterval: t.Dispatch, RenewInterval: t.Renew, ReapInterval: t.Reap, CancelInterval: t.Cancel, IncapableRetry: t.IncapableRetry, LeaseTTL: t.LeaseTTL,
	}
	dcfg.Nodes, dcfg.Secrets, dcfg.Project = n.dispatchSources(cfg, log)
	if hk, ok := dcfg.Nodes.(dispatch.HostKeyRecorder); ok {
		dcfg.HostKeys = hk
	}
	if n.disp, err = dispatch.New(dcfg); err != nil {
		return fmt.Errorf("creating dispatcher: %w", err)
	}
	for _, h := range []controller.SessionHook{n.logs, n.disp} {
		if err := gw.AddHook(h); err != nil {
			return err
		}
	}
	if err := n.setupTail(cfg); err != nil {
		return errors.Join(err, n.stopOrchestration(context.WithoutCancel(ctx)))
	}
	// Each step is started in order; on failure the ones already running stop.
	for _, step := range []struct {
		name  string
		start func(context.Context) error
	}{{"run.advance", n.adv.Start}, {"log ingest", n.logs.Start}, {"leader elector", n.lead.Start}, {"dispatcher", n.disp.Start}, {"internal listener", n.startInternal}, {"agent gateway", gw.Start}} {
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
		return errors.Join(n.gw.Stop(ctx), n.stopGuard(ctx))
	}
	return errors.Join(n.stopInternal(ctx), n.disp.Stop(ctx), n.lead.Stop(ctx), n.gw.Stop(ctx), n.logs.Stop(ctx), n.adv.Stop(ctx), n.stopGuard(ctx))
}

// setupTail chooses the live log tail of the API: this controller's own buffer,
// or, with a cluster secret, one that also relays the tail of executions owned
// by other controllers. The internal listener (serving ours to them) is
// started with the other components.
func (n *Node) setupTail(cfg Config) error {
	if cfg.ClusterSecret == "" {
		n.tail = n.logs
		return nil
	}
	keys, err := internalauth.NewKeyring(cfg.ClusterSecret, cfg.ClusterSecretPrevious)
	if err != nil {
		return fmt.Errorf("building cluster keyring: %w", err)
	}
	if n.prox, err = tailproxy.New(tailproxy.Config{
		Local: n.logs, Store: cfg.Store, Self: cfg.ControllerID, Keys: keys, Logger: cfg.Logger,
	}); err != nil {
		return fmt.Errorf("creating log tail proxy: %w", err)
	}
	n.tail = n.prox
	if n.intl, err = internalapi.New(internalapi.Config{Keys: keys, Self: cfg.ControllerID, Tail: n.logs, Logger: cfg.Logger}); err != nil {
		return fmt.Errorf("creating internal listener: %w", err)
	}
	n.intL = cfg.InternalListener
	if n.intL == nil {
		if n.intL, err = net.Listen("tcp", cfg.InternalListen); err != nil {
			return fmt.Errorf("listening on the internal address %q: %w", cfg.InternalListen, err)
		}
	}
	return nil
}

func (n *Node) startInternal(context.Context) error {
	if n.intl != nil {
		n.intl.Serve(n.intL)
	}
	return nil
}

func (n *Node) stopInternal(ctx context.Context) error {
	if n.prox != nil {
		defer n.prox.Close()
	}
	if n.intl == nil {
		return nil
	}
	err := n.intl.Stop(ctx)
	_ = n.intL.Close() // already closed once served; frees the port if startup failed first
	return err
}

// sqliteGuard is the lease that keeps a second controller off a SQLite database.
const sqliteGuard = "sqlite-controller"

// guardSingleController takes the single-controller lease, waiting out the
// lease of a controller that crashed. A live controller on the same database
// makes the start fail.
func (n *Node) guardSingleController(ctx context.Context, cfg Config) error {
	t := cfg.Timing
	g, err := leader.New(leader.Config{Store: cfg.Store, Name: sqliteGuard, Holder: cfg.ControllerID, TTL: t.LeaderTTL, Renew: t.LeaderRenew, Logger: cfg.Logger})
	if err != nil {
		return fmt.Errorf("creating the single-controller guard: %w", err)
	}
	ttl, renew := t.LeaderTTL, t.LeaderRenew
	if ttl <= 0 {
		ttl = leader.DefaultTTL
	}
	if renew <= 0 {
		renew = leader.DefaultRenew
	}
	if err := g.StartExclusive(ctx, ttl+renew); err != nil {
		if errors.Is(err, leader.ErrHeld) {
			return errors.New("another controller is using this SQLite database; SQLite supports one controller, use PostgreSQL for several")
		}
		return fmt.Errorf("taking the single-controller lease: %w", err)
	}
	n.only = g
	return nil
}

func (n *Node) stopGuard(ctx context.Context) error {
	if n.only == nil {
		return nil
	}
	return n.only.Stop(ctx)
}

// dispatchSources picks where the dispatcher reads nodes and secrets, and the
// project lookup that authorizes secret refs: nodes come from the inventory,
// secrets from the db backend when a KEK is configured, and refs are then
// authorized against the run's project (the --dev-nodes file is seeded into
// these stores, ADR-0044).
func (n *Node) dispatchSources(cfg Config, log *slog.Logger) (dispatch.NodeSource, dispatch.SecretSource, func(context.Context, string) (string, error)) {
	nodes := &InventoryNodes{Store: cfg.Store, Scope: inventory.AllNodes{}, Project: projectOf(cfg.Store), Log: log}
	if n.secrets == nil {
		return nodes, nil, nil
	}
	return nodes, &secretSource{b: n.secrets, log: log}, projectOf(cfg.Store)
}
