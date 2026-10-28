package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/denyszorinets/damson/internal/controller/advance"
	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/store"
)

// startAdvancer builds and starts the run.advance workers on the migrated
// store (ADR-0010).
func startAdvancer(ctx context.Context, st store.Store, log *slog.Logger) (*advance.Advancer, error) {
	eng, err := engine.New(expr.Limits{})
	if err != nil {
		return nil, fmt.Errorf("building engine: %w", err)
	}
	adv, err := advance.New(advance.Config{Store: st, Engine: eng, Logger: log})
	if err != nil {
		return nil, fmt.Errorf("creating run advancer: %w", err)
	}
	if err := adv.Start(ctx); err != nil {
		return nil, err
	}
	return adv, nil
}

// stopAdvancer drains the workers; a nil advancer (unmigrated database) is fine.
func stopAdvancer(ctx context.Context, adv *advance.Advancer) error {
	if adv == nil {
		return nil
	}
	return adv.Stop(ctx)
}
