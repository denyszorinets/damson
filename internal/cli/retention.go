package cli

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/controller/leader"
	"github.com/denyszorinets/damson/internal/controller/retention"
	"github.com/denyszorinets/damson/internal/store"
)

// startRetention starts the retention duty and its lease when a retention is
// configured (ADR-0054) and returns the function that stops both. With
// audit.retention_days = 0 it starts nothing.
func startRetention(ctx context.Context, st store.Store, cfg config.Config, id string, log *slog.Logger) (func(), error) {
	if cfg.Audit.RetentionDays == 0 {
		return func() {}, nil
	}
	el, err := leader.New(leader.Config{Store: st, Name: retention.LeaderName, Holder: id, Logger: log})
	if err != nil {
		return nil, fmt.Errorf("preparing the retention lease: %w", err)
	}
	ret, err := retention.New(retention.Config{Store: st, Leader: el, Holder: id, AuditDays: cfg.Audit.RetentionDays, Logger: log})
	if err != nil {
		return nil, fmt.Errorf("preparing the retention duty: %w", err)
	}
	if err := el.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting the retention lease: %w", err)
	}
	if err := ret.Start(ctx); err != nil {
		_ = el.Stop(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("starting the retention duty: %w", err)
	}
	return func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err := ret.Stop(sctx); err != nil {
			log.Warn("stopping the retention duty", "error", err)
		}
		if err := el.Stop(sctx); err != nil {
			log.Warn("releasing the retention lease", "error", err)
		}
	}, nil
}
