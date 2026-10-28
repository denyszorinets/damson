package api_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres"
	"github.com/denyszorinets/damson/internal/store/sqlite"
)

// backend is one database the api tests run against.
type backend struct {
	name string
	open func(t *testing.T) store.Store
}

// backends lists SQLite plus, under the integration tag, Postgres.
func backends() []backend {
	return append([]backend{{name: "sqlite", open: openSQLite}}, postgresBackends()...)
}

// forEachBackend runs fn as a subtest per backend with a fresh, migrated store.
func forEachBackend(t *testing.T, fn func(t *testing.T, st store.Store)) {
	t.Helper()
	for _, be := range backends() {
		t.Run(be.name, func(t *testing.T) { fn(t, be.open(t)) })
	}
}

func openSQLite(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	return migrate(t, st)
}

func openPostgres(t *testing.T, dsn string) store.Store {
	t.Helper()
	st, err := postgres.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	return migrate(t, st)
}

func migrate(t *testing.T, st store.Store) store.Store {
	t.Helper()
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}
