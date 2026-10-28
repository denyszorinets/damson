// Package postgres is the PostgreSQL (pgx v5) store backend for production
// and HA installs.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/migrations"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

// uniqueViolation is the SQLSTATE for unique_violation.
const uniqueViolation = "23505"

// Store implements store.Store on PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
	sql  *sql.DB // database/sql view of the pool, used by the migration runner
	q    *gen.Queries
}

var _ store.Store = (*Store)(nil)

// Open connects to the server at dsn (a postgres:// URL or key=value string).
// Errors never include the DSN, which may embed a password.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("opening postgres: invalid dsn")
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("opening postgres pool: %w", err)
	}
	s := &Store{pool: pool, sql: stdlib.OpenDBFromPool(pool), q: gen.New(pool)}
	if err := s.Ping(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// Ping implements store.Store.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("pinging postgres: %w", err)
	}
	return nil
}

// Migrate implements store.Store. It holds a Postgres advisory lock so that
// concurrent controllers serialize. It also applies River's own migrations
// (ADR-0010) to the same database.
func (s *Store) Migrate(ctx context.Context) error {
	if err := migrations.Up(ctx, s.sql, migrations.Postgres); err != nil {
		return err
	}
	return migrations.RiverUpPostgres(ctx, s.pool)
}

// MigrationVersion implements store.Store.
func (s *Store) MigrationVersion(ctx context.Context) (int64, error) {
	return migrations.Version(ctx, s.sql, migrations.Postgres)
}

// Close implements store.Store.
func (s *Store) Close() error {
	_ = s.sql.Close()
	s.pool.Close()
	return nil
}

// GetSetting implements store.Store.
func (s *Store) GetSetting(ctx context.Context, key string) (store.Setting, error) {
	row, err := s.q.GetSetting(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Setting{}, fmt.Errorf("setting %q: %w", key, store.ErrNotFound)
	}
	if err != nil {
		return store.Setting{}, fmt.Errorf("getting setting %q: %w", key, err)
	}
	return store.Setting{Key: row.SettingKey, Value: row.SettingValue, Version: row.Version}, nil
}

// CreateSetting implements store.Store.
func (s *Store) CreateSetting(ctx context.Context, key, value string) (store.Setting, error) {
	_, err := s.q.InsertSetting(ctx, gen.InsertSettingParams{SettingKey: key, SettingValue: value})
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == uniqueViolation {
		return store.Setting{}, fmt.Errorf("setting %q: %w", key, store.ErrConflict)
	}
	if err != nil {
		return store.Setting{}, fmt.Errorf("creating setting %q: %w", key, err)
	}
	return store.Setting{Key: key, Value: value, Version: 1}, nil
}

// UpdateSetting implements store.Store.
func (s *Store) UpdateSetting(ctx context.Context, key, value string, expected int64) (store.Setting, error) {
	n, err := s.q.UpdateSettingCAS(ctx, gen.UpdateSettingCASParams{
		SettingValue: value, SettingKey: key, ExpectedVersion: expected,
	})
	if err != nil {
		return store.Setting{}, fmt.Errorf("updating setting %q: %w", key, err)
	}
	if n == 0 {
		if _, err := s.GetSetting(ctx, key); err != nil {
			return store.Setting{}, err
		}
		return store.Setting{}, fmt.Errorf("setting %q at version %d: %w", key, expected, store.ErrStale)
	}
	return store.Setting{Key: key, Value: value, Version: expected + 1}, nil
}
