package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivermigrate"
)

// riverLockKey is the Postgres advisory lock that serializes River's own
// migrations across controllers. It differs from goose's lock key, which is
// already released when this runs.
const riverLockKey int64 = 0x44414d534f4e5231 // "DAMSONR1"

// RiverUpPostgres applies River's migrations (its own schema, tracked in
// river_migration) to the database behind pool, in one transaction that holds
// a transaction-scoped advisory lock: one connection, and the lock disappears
// with the transaction even if something fails. Safe to call concurrently from
// several controllers and idempotent (ADR-0010).
func RiverUpPostgres(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning river migration: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", riverLockKey); err != nil {
		return fmt.Errorf("locking river migrations: %w", err)
	}
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("creating river migrator: %w", err)
	}
	if _, err := m.MigrateTx(ctx, tx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("applying river postgres migrations: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing river migrations: %w", err)
	}
	return nil
}

// RiverUpSQLite applies River's migrations to the SQLite database behind db.
// The caller serializes calls per database file (SQLite is single-controller).
func RiverUpSQLite(ctx context.Context, db *sql.DB) error {
	m, err := rivermigrate.New(riversqlite.New(db), nil)
	if err != nil {
		return fmt.Errorf("creating river migrator: %w", err)
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("applying river sqlite migrations: %w", err)
	}
	return nil
}
