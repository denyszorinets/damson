package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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

// orchestration is the controller's run machinery: run.advance workers, log
// ingest, dispatch and the agent gateway (ADR-0010, ARCHITECTURE 8.3).
type orchestration struct {
	gw   *controller.Gateway
	adv  *advance.Advancer
	logs *logs.Ingest
	disp *dispatch.Dispatcher
}

// startOrchestration builds the components on the migrated store, hooks log
// ingest and step.complete into the gateway and starts everything. Agents are
// accepted last, once every handler exists.
func startOrchestration(ctx context.Context, st store.Store, gw *controller.Gateway, plugins *pluginreg.Registry, controllerID string, devNodes *devnodes.File, log *slog.Logger) (*orchestration, error) {
	eng, err := engine.New(expr.Limits{})
	if err != nil {
		return nil, fmt.Errorf("building engine: %w", err)
	}
	adv, err := advance.New(advance.Config{Store: st, Engine: eng, Logger: log})
	if err != nil {
		return nil, fmt.Errorf("creating run advancer: %w", err)
	}
	ing, err := logs.New(logs.Config{Store: st, Logger: log, Acks: func(ctx context.Context, agentID string, ack protocol.LogAck) error {
		peer, ok := gw.Peer(agentID)
		if !ok {
			return errors.New("agent not connected")
		}
		return peer.Notify(ctx, protocol.MethodLogAck, ack)
	}})
	if err != nil {
		return nil, fmt.Errorf("creating log ingest: %w", err)
	}
	dcfg := dispatch.Config{
		Store: st, Agents: gw, Plugins: plugins, Advance: adv, ControllerID: controllerID, Logger: log, Invalidate: ing.Invalidate, Finish: ing.Finish,
	}
	if devNodes != nil { // a nil *File must not become a non-nil interface
		dcfg.Nodes, dcfg.Secrets = devNodes, devNodes
	}
	disp, err := dispatch.New(dcfg)
	if err != nil {
		return nil, fmt.Errorf("creating dispatcher: %w", err)
	}
	o := &orchestration{gw: gw, adv: adv, logs: ing, disp: disp}
	for _, h := range []controller.SessionHook{ing, disp} {
		if err := gw.AddHook(h); err != nil {
			return nil, err
		}
	}
	// Each step is started in order; on failure the ones already running stop.
	for _, step := range []struct {
		name  string
		start func(context.Context) error
	}{{"run.advance", adv.Start}, {"log ingest", ing.Start}, {"dispatcher", disp.Start}, {"agent gateway", gw.Start}} {
		if err := step.start(ctx); err != nil {
			sctx := context.WithoutCancel(ctx)
			return nil, errors.Join(fmt.Errorf("starting %s: %w", step.name, err), o.stop(sctx))
		}
	}
	return o, nil
}

// advancer returns the run.advance component, or nil for a nil orchestration
// (unmigrated database).
func (o *orchestration) advancer() *advance.Advancer {
	if o == nil {
		return nil
	}
	return o.adv
}

// stop shuts down in reverse dependency order: no new dispatches, agents told
// to reconnect, buffered logs flushed, then the run.advance workers drain.
// Stopping a component that never started is a no-op.
func (o *orchestration) stop(ctx context.Context) error {
	if o == nil {
		return nil
	}
	return errors.Join(o.disp.Stop(ctx), o.gw.Stop(ctx), o.logs.Stop(ctx), o.adv.Stop(ctx))
}

// loadDevNodes loads the --dev-nodes file, or returns nil without a path. It
// warns loudly: the file replaces inventory and secret authorization (ADR-0034).
func loadDevNodes(path string, log *slog.Logger) (*devnodes.File, error) {
	if path == "" {
		return nil, nil
	}
	f, err := devnodes.Load(path)
	if err != nil {
		return nil, fmt.Errorf("loading --dev-nodes: %w", err)
	}
	log.Warn("dev nodes: no inventory, no secret authorization; development only", "path", path, "nodes", f.Names())
	return f, nil
}
