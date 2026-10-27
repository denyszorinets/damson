package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/config"
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

	srv := api.NewServer(api.Config{
		Addr: cfg.Controller.Listen, Store: st, UI: webui.Handler(web.Build()), Logger: log,
	})
	if err := srv.Start(ctx); err != nil {
		return err
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
	return errors.Join(serveErr, srv.Stop(sctx))
}
