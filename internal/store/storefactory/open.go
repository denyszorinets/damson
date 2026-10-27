// Package storefactory opens the store backend selected by configuration.
// It is separate from package store so the backends can import store's
// sentinel errors without an import cycle.
package storefactory

import (
	"context"
	"fmt"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres"
	"github.com/denyszorinets/damson/internal/store/sqlite"
)

// Open connects to the database described by cfg. It does not migrate.
func Open(ctx context.Context, cfg config.Database) (store.Store, error) {
	switch cfg.Driver {
	case config.DriverSQLite:
		return sqlite.Open(ctx, cfg.DSN)
	case config.DriverPostgres:
		return postgres.Open(ctx, cfg.DSN)
	default:
		return nil, fmt.Errorf("unknown database driver %q", cfg.Driver)
	}
}
