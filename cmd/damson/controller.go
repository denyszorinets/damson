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

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/controller"
	"github.com/denyszorinets/damson/internal/store/storefactory"
	"github.com/denyszorinets/damson/internal/webui"
	"github.com/denyszorinets/damson/web"
)

// shutdownTimeout bounds how long in-flight requests may drain after a signal.
const shutdownTimeout = 15 * time.Second

// runController opens the store, optionally migrates it, serves the HTTP API
// on the configured address and blocks until ctx is cancelled. It then shuts
// down in reverse order: HTTP server first, then the store.
func runController(ctx context.Context, cfg config.Config, log *slog.Logger, migrate bool) error {
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

	id, err := controllerID()
	if err != nil {
		return err
	}
	gw, err := controller.New(controller.Config{
		Store: st, ControllerID: id, InternalURL: cfg.Controller.InternalURL,
		InsecureDev: cfg.Controller.InsecureDev, TrustForwardedProto: cfg.Controller.TrustForwardedProto, Logger: log,
	})
	if err != nil {
		return fmt.Errorf("creating agent gateway: %w", err)
	}
	// An unmigrated database has no agent tables: keep serving health
	// endpoints, but leave the gateway stopped (it answers 503) until the
	// operator migrates and restarts.
	if v, err := st.MigrationVersion(ctx); err != nil {
		return fmt.Errorf("reading migration version: %w", err)
	} else if v == 0 {
		log.Warn("database is not migrated; agent gateway disabled (run with --auto-migrate)")
	} else if err := gw.Start(ctx); err != nil {
		return err
	}
	srv := api.NewServer(api.Config{
		Addr: cfg.Controller.Listen, Store: st, UI: webui.Handler(web.Build()), Logger: log,
		Mounts: map[string]http.Handler{controller.ConnectPath: gw.Handler()},
	})
	if err := srv.Start(ctx); err != nil {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		return errors.Join(err, gw.Stop(sctx))
	}
	var serveErr error
	select {
	case <-ctx.Done():
		log.Info("stopping")
	case serveErr = <-srv.Err():
		log.Error("http server failed, stopping", "error", serveErr)
	}

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	// Stop the listener first so no new agents arrive, then tell the connected
	// ones to reconnect elsewhere and deregister (ARCHITECTURE 8.5).
	return errors.Join(serveErr, srv.Stop(sctx), gw.Stop(sctx))
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
