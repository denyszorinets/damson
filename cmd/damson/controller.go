package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/denyszorinets/damson/internal/agent"
	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/controller"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store/storefactory"
	"github.com/denyszorinets/damson/internal/webui"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/all"
	"github.com/denyszorinets/damson/web"
)

// shutdownTimeout bounds how long in-flight requests may drain after a signal.
const shutdownTimeout = 15 * time.Second

// runController opens the store, optionally migrates it, serves the HTTP API
// on the configured address and blocks until ctx is cancelled. It then shuts
// down in reverse order: in-process agent (dev only), HTTP server, gateway,
// then the store. With dev set it also starts an agent in this process,
// connected to the gateway through a freshly provisioned pool token.
func runController(ctx context.Context, cfg config.Config, log *slog.Logger, migrate, dev bool, devNodesPath string) error {
	st, err := storefactory.Open(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Warn("closing store", "error", err)
		}
	}()
	if migrate {
		if err := st.Migrate(ctx); err != nil {
			return fmt.Errorf("migrating database: %w", err)
		}
		log.Info("migrations applied")
	}

	devNodes, err := loadDevNodes(devNodesPath, log)
	if err != nil {
		return err
	}
	id, err := controllerID()
	if err != nil {
		return err
	}
	plugins, err := newPluginRegistry(log)
	if err != nil {
		return err
	}
	gw, err := controller.New(controller.Config{
		Store: st, Plugins: plugins, ControllerID: id, InternalURL: cfg.Controller.InternalURL,
		InsecureDev: cfg.Controller.InsecureDev, TrustForwardedProto: cfg.Controller.TrustForwardedProto, Logger: log,
	})
	if err != nil {
		return fmt.Errorf("creating agent gateway: %w", err)
	}
	var orch *orchestration
	// An unmigrated database has no agent tables: keep serving health
	// endpoints, but leave the gateway stopped (it answers 503) until the
	// operator migrates and restarts.
	if v, err := st.MigrationVersion(ctx); err != nil {
		return fmt.Errorf("reading migration version: %w", err)
	} else if v == 0 {
		log.Warn("database is not migrated; agent gateway and run.advance disabled (run with --auto-migrate)")
	} else if orch, err = startOrchestration(ctx, st, gw, plugins, id, devNodes, log); err != nil {
		return err
	}
	adv := orch.advancer()
	apiCfg := api.Config{
		Addr: cfg.Controller.Listen, Store: st, Data: st, Plugins: plugins, UI: webui.Handler(web.Build()), Logger: log,
		Mounts: map[string]http.Handler{controller.ConnectPath: gw.Handler()},
	}
	if adv != nil {
		apiCfg.Advance, apiCfg.RunNotify = adv, adv.Hub()
	}
	srv := api.NewServer(apiCfg)
	if err := srv.Start(ctx); err != nil {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		return errors.Join(err, orch.stop(sctx))
	}
	var devAgent *agent.Agent
	if dev {
		if devAgent, err = startDevAgent(ctx, st, cfg, srv.Addr(), log); err != nil {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
			defer cancel()
			if ctx.Err() != nil && errors.Is(err, context.Canceled) {
				err = nil // shut down while provisioning: a clean stop
			}
			return errors.Join(err, srv.Stop(sctx), orch.stop(sctx))
		}
	}
	serveErr := awaitShutdown(ctx, log, srv.Err(), devAgent)

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	// Stop the dev agent and the listener first so no new agents arrive, then
	// tell the connected ones to reconnect elsewhere and deregister
	// (ARCHITECTURE 8.5).
	return errors.Join(serveErr, stopDevAgent(sctx, devAgent, log), srv.Stop(sctx), orch.stop(sctx))
}

// controllerID is the hostname plus a boot nonce, unique per process.
func controllerID() (string, error) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "controller"
	}
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generating controller id: %w", err)
	}
	return host + "-" + hex.EncodeToString(nonce[:]), nil
}

// awaitShutdown blocks until ctx is cancelled, the HTTP server fails or the
// in-process dev agent (nil outside dev) ends fatally, and returns the error
// that ended it, if any.
func awaitShutdown(ctx context.Context, log *slog.Logger, srvErr <-chan error, devAgent *agent.Agent) error {
	var agentDone <-chan struct{}
	if devAgent != nil {
		agentDone = devAgent.Done()
	}
	select {
	case <-ctx.Done():
		log.Info("stopping")
		return nil
	case err := <-srvErr:
		log.Error("http server failed, stopping", "error", err)
		return err
	case <-agentDone:
		if devAgent.Err() == nil {
			// The agent exits cleanly when ctx is cancelled; that is a normal shutdown.
			log.Info("stopping")
			return nil
		}
		err := fmt.Errorf("dev agent stopped: %w", devAgent.Err())
		log.Error("dev agent ended, stopping", "error", err)
		return err
	}
}

// newPluginRegistry links the built-in plugins into a registry, so their
// schemas are known before any agent connects (ARCHITECTURE section 6).
func newPluginRegistry(log *slog.Logger) (*pluginreg.Registry, error) {
	var ms []pluginsdk.Manifest
	for _, p := range all.Plugins() {
		ms = append(ms, p.Manifest())
	}
	reg, err := pluginreg.New(ms, log)
	if err != nil {
		return nil, fmt.Errorf("building plugin registry: %w", err)
	}
	return reg, nil
}
