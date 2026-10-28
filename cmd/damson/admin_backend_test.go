package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/storefactory"
)

// adminBackend is one database the admin tests run against.
type adminBackend struct {
	name   string
	driver string
	// newDSN returns the DSN of a fresh, empty database.
	newDSN func(t *testing.T) string
}

// adminBackends lists SQLite plus, under the integration tag, Postgres.
func adminBackends() []adminBackend {
	bs := []adminBackend{{name: "sqlite", driver: config.DriverSQLite, newDSN: func(t *testing.T) string {
		return filepath.Join(t.TempDir(), "admin.db")
	}}}
	return append(bs, postgresBackends()...)
}

// forEachBackend runs fn as a subtest per backend.
func forEachBackend(t *testing.T, fn func(t *testing.T, be adminBackend)) {
	t.Helper()
	for _, be := range adminBackends() {
		t.Run(be.name, func(t *testing.T) { fn(t, be) })
	}
}

// adminEnv returns env for a fresh database, migrated when migrate is true.
func adminEnv(t *testing.T, be adminBackend, migrate bool) []string {
	t.Helper()
	dsn := be.newDSN(t)
	if migrate {
		st, err := storefactory.Open(context.Background(), config.Database{Driver: be.driver, DSN: dsn})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return []string{"DAMSON_DATABASE__DRIVER=" + be.driver, "DAMSON_DATABASE__DSN=" + dsn}
}

// openStore opens the database named by env, as built by adminEnv.
func openStore(t *testing.T, be adminBackend, env []string) store.Store {
	t.Helper()
	st, err := storefactory.Open(context.Background(), config.Database{Driver: be.driver, DSN: env[1][len("DAMSON_DATABASE__DSN="):]})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
