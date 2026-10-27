package sqlite_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
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

func TestConcurrentMigrateAcrossSpellings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "damson.db")
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	// The file must exist for the symlink to resolve.
	seed, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()
	spellings := []string{path, "file:" + path + "?cache=shared", link}

	const n = 8
	stores := make([]store.Store, n)
	for i := range stores {
		s, err := sqlite.Open(ctx, spellings[i%len(spellings)])
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		stores[i] = s
	}
	start := make(chan struct{})
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for _, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- s.Migrate(ctx)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Migrate: %v", err)
		}
	}
	v, err := stores[0].MigrationVersion(ctx)
	if err != nil || v < 1 {
		t.Fatalf("MigrationVersion = %d, %v; want >= 1", v, err)
	}
}
