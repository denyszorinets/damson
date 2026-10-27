// Package migrations embeds the per-dialect SQL migrations and runs them with
// goose. Applied versions are tracked in the schema_migrations table.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed postgres/*.sql sqlite/*.sql
var files embed.FS

// Table is the migration bookkeeping table.
const Table = "schema_migrations"

// Dialect names a migration directory.
type Dialect string

// Supported dialects.
const (
	Postgres Dialect = "postgres"
	SQLite   Dialect = "sqlite"
)

func provider(db *sql.DB, d Dialect) (*goose.Provider, error) {
	sub, err := fs.Sub(files, string(d))
	if err != nil {
		return nil, fmt.Errorf("opening %s migrations: %w", d, err)
	}
	opts := []goose.ProviderOption{goose.WithTableName(Table)}
	gd := goose.DialectSQLite3
	if d == Postgres {
		gd = goose.DialectPostgres
		locker, err := lock.NewPostgresSessionLocker()
		if err != nil {
			return nil, fmt.Errorf("creating advisory locker: %w", err)
		}
		opts = append(opts, goose.WithSessionLocker(locker))
	}
	p, err := goose.NewProvider(gd, db, sub, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating %s migration provider: %w", d, err)
	}
	return p, nil
}

// Up applies all pending migrations.
func Up(ctx context.Context, db *sql.DB, d Dialect) error {
	p, err := provider(db, d)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("applying %s migrations: %w", d, err)
	}
	return nil
}

func tableExists(ctx context.Context, db *sql.DB, d Dialect) (bool, error) {
	q := "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?"
	if d == Postgres {
		q = "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1"
	}
	var n int
	if err := db.QueryRowContext(ctx, q, Table).Scan(&n); err != nil {
		return false, fmt.Errorf("checking %s migration table: %w", d, err)
	}
	return n > 0, nil
}

// Version returns the highest applied migration version, 0 if none. It never
// creates the bookkeeping table: a database that was not migrated is version 0.
func Version(ctx context.Context, db *sql.DB, d Dialect) (int64, error) {
	ok, err := tableExists(ctx, db, d)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	p, err := provider(db, d)
	if err != nil {
		return 0, err
	}
	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading %s migration version: %w", d, err)
	}
	return v, nil
}
