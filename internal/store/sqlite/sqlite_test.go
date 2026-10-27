package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite"
	"github.com/denyszorinets/damson/internal/store/storetest"
)

func TestSuite(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "damson.db"))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestOpenEmptyDSN(t *testing.T) {
	if _, err := sqlite.Open(context.Background(), ""); err == nil {
		t.Fatal("Open with empty dsn succeeded")
	}
}

func TestOpenRejectsMemory(t *testing.T) {
	for _, dsn := range []string{":memory:", "file::memory:?cache=shared", "file:x?mode=memory"} {
		if _, err := sqlite.Open(context.Background(), dsn); err == nil {
			t.Errorf("Open(%q) succeeded", dsn)
		}
	}
}

func TestVersionFreshDBHasNoSideEffects(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "damson.db")
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	v, err := s.MigrationVersion(ctx)
	if err != nil || v != 0 {
		t.Fatalf("MigrationVersion = %d, %v; want 0, nil", v, err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	var n int
	if err := raw.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'schema_migrations'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("MigrationVersion created schema_migrations")
	}
}
