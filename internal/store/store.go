// Package store defines the persistence interface and its sentinel errors.
// Implementations live in the postgres and sqlite subpackages; storefactory
// selects one from configuration. The database is the only source of truth
// (ARCHITECTURE §8.1): every state transition is a compare-and-swap with a
// rows-affected check.
package store

import (
	"context"
	"errors"
)

var (
	// ErrNotFound means the addressed row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict means a uniqueness constraint rejected the write.
	ErrConflict = errors.New("store: conflict")
	// ErrStale means a compare-and-swap lost: the row exists but its
	// expected state or version no longer matches.
	ErrStale = errors.New("store: stale")
)

// Setting is an instance-wide key/value pair guarded by an optimistic
// version counter.
type Setting struct {
	Key     string
	Value   string
	Version int64
}

// Store is the persistence boundary. Both backends pass the same suite in
// storetest.
type Store interface {
	// Ping verifies the database is reachable.
	Ping(ctx context.Context) error
	// Migrate applies all pending migrations. It is safe to call from
	// several controllers at once (Postgres uses an advisory lock).
	Migrate(ctx context.Context) error
	// MigrationVersion returns the highest applied migration, 0 if none.
	MigrationVersion(ctx context.Context) (int64, error)
	// Close releases the connections.
	Close() error

	// GetSetting returns ErrNotFound if the key is absent.
	GetSetting(ctx context.Context, key string) (Setting, error)
	// CreateSetting inserts at version 1 and returns ErrConflict if the key exists.
	CreateSetting(ctx context.Context, key, value string) (Setting, error)
	// UpdateSetting is a CAS on version. It returns ErrNotFound if the key
	// is absent and ErrStale if expectedVersion is not current.
	UpdateSetting(ctx context.Context, key, value string, expectedVersion int64) (Setting, error)
}
