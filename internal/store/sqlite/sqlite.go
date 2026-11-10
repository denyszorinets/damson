// Package sqlite is the SQLite (modernc.org/sqlite, pure Go) store backend for
// development and single-controller installs.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sqlitedrv "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/migrations"
	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

// Store implements store.Store on SQLite.
type Store struct {
	db      *sql.DB
	q       *gen.Queries
	lockKey string
	// queueDB is the pool River uses: one connection, because River issues
	// many concurrent writes and SQLite allows one writer (riversqlite docs).
	// It is a second pool on the same file; transactional enqueues still run
	// on the caller's own transaction from db.
	queueDB *sql.DB
}

var _ store.Store = (*Store)(nil)

// Open connects to the database at dsn: a file path or a file: URI. Foreign
// keys, WAL, a busy timeout and immediate write transactions are enabled.
// In-memory databases are rejected: each pooled connection would see its own
// empty database. If SQLITE_BUSY persists past the busy timeout it surfaces as
// a wrapped driver error, not as a store sentinel.
func Open(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("opening sqlite: empty dsn")
	}
	if isMemoryDSN(dsn) {
		return nil, errors.New("opening sqlite: in-memory databases are not supported; use a file path")
	}
	db, err := sql.Open("sqlite", withPragmas(dsn))
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}
	queueDB, err := sql.Open("sqlite", withPragmas(dsn))
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("opening sqlite queue pool: %w", err)
	}
	queueDB.SetMaxOpenConns(1)
	s := &Store{db: db, q: gen.New(db), queueDB: queueDB}
	if err := s.Ping(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	// Computed after Ping on purpose: the file exists by now, so symlinks
	// resolve to the real path.
	s.lockKey = canonicalPath(dsn)
	return s, nil
}

func isMemoryDSN(dsn string) bool {
	return strings.Contains(dsn, ":memory:") || strings.Contains(dsn, "mode=memory")
}

func withPragmas(dsn string) string {
	if !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
}

// Ping implements store.Store.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("pinging sqlite: %w", err)
	}
	return nil
}

// Migrate implements store.Store.
//
// SQLite has no advisory lock, so calls are serialized in-process per database
// file. Concurrent migration from several processes is not supported (SQLite
// is single-controller). It also applies River's own migrations (ADR-0010).
func (s *Store) Migrate(ctx context.Context) error {
	unlock := lockMigrations(s.lockKey)
	defer unlock()
	if err := migrations.Up(ctx, s.db, migrations.SQLite); err != nil {
		return err
	}
	return migrations.RiverUpSQLite(ctx, s.queueDB)
}

// MigrationVersion implements store.Store.
func (s *Store) MigrationVersion(ctx context.Context) (int64, error) {
	return migrations.Version(ctx, s.db, migrations.SQLite)
}

// Close implements store.Store.
func (s *Store) Close() error {
	return errors.Join(s.queueDB.Close(), s.db.Close())
}

// QueueDB returns the single-connection pool for the River SQLite driver.
func (s *Store) QueueDB() *sql.DB { return s.queueDB }

// GetSetting implements store.Store.
func (s *Store) GetSetting(ctx context.Context, key string) (store.Setting, error) {
	row, err := s.q.GetSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Setting{}, fmt.Errorf("setting %q: %w", key, store.ErrNotFound)
	}
	if err != nil {
		return store.Setting{}, fmt.Errorf("getting setting %q: %w", key, err)
	}
	return toSetting(row), nil
}

// CreateSetting implements store.Store.
func (s *Store) CreateSetting(ctx context.Context, key, value string) (store.Setting, error) {
	_, err := s.q.InsertSetting(ctx, gen.InsertSettingParams{SettingKey: key, SettingValue: value})
	if isUnique(err) {
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

func toSetting(r gen.InstanceSetting) store.Setting {
	return store.Setting{Key: r.SettingKey, Value: r.SettingValue, Version: r.Version}
}

func isForeignKey(err error) bool {
	var e *sqlitedrv.Error
	return errors.As(err, &e) && e.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
}

func isUnique(err error) bool {
	var e *sqlitedrv.Error
	if !errors.As(err, &e) {
		return false
	}
	c := e.Code()
	return c == sqlite3.SQLITE_CONSTRAINT_UNIQUE || c == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}
