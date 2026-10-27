// Package storetest holds the store contract suite. Each backend runs it
// against a fresh, unmigrated database (ARCHITECTURE §20).
package storetest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
)

// Factory returns a store on a fresh, empty database, closed on cleanup.
type Factory func(t *testing.T) store.Store

// Run executes every contract test against stores from f.
func Run(t *testing.T, f Factory) {
	t.Helper()
	tests := []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"Ping", testPing},
		{"PingCancelled", testPingCancelled},
		{"MigrateIdempotent", testMigrate},
		{"MigrateConcurrent", testMigrateConcurrent},
		{"Settings", testSettings},
		{"CASSingleWinner", testCASSingleWinner},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.fn(t, f(t)) })
	}
}

func migrated(t *testing.T, s store.Store) {
	t.Helper()
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
}

func testPing(t *testing.T, s store.Store) {
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func testPingCancelled(t *testing.T, s store.Store) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Ping(ctx); err == nil {
		t.Fatal("Ping with cancelled context succeeded")
	}
}

func testMigrate(t *testing.T, s store.Store) {
	ctx := context.Background()
	if v, err := s.MigrationVersion(ctx); err != nil || v != 0 {
		t.Fatalf("fresh MigrationVersion = %d, %v; want 0, nil", v, err)
	}
	for i := range 2 { // the second run must be a no-op
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("Migrate #%d: %v", i+1, err)
		}
	}
	if v, err := s.MigrationVersion(ctx); err != nil || v < 1 {
		t.Fatalf("MigrationVersion = %d, %v; want >= 1, nil", v, err)
	}
}

func testMigrateConcurrent(t *testing.T, s store.Store) {
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.Migrate(context.Background())
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Migrate #%d: %v", i, err)
		}
	}
}

func testSettings(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)

	if _, err := s.GetSetting(ctx, "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get missing: %v; want ErrNotFound", err)
	}
	if _, err := s.UpdateSetting(ctx, "a", "x", 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update missing: %v; want ErrNotFound", err)
	}

	got, err := s.CreateSetting(ctx, "a", "one")
	if err != nil || got != (store.Setting{Key: "a", Value: "one", Version: 1}) {
		t.Fatalf("Create = %+v, %v", got, err)
	}
	if _, err := s.CreateSetting(ctx, "a", "dup"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Create duplicate: %v; want ErrConflict", err)
	}

	got, err = s.UpdateSetting(ctx, "a", "two", 1)
	if err != nil || got != (store.Setting{Key: "a", Value: "two", Version: 2}) {
		t.Fatalf("Update = %+v, %v", got, err)
	}
	if _, err := s.UpdateSetting(ctx, "a", "stale", 1); !errors.Is(err, store.ErrStale) {
		t.Fatalf("Update stale: %v; want ErrStale", err)
	}

	got, err = s.GetSetting(ctx, "a")
	if err != nil || got != (store.Setting{Key: "a", Value: "two", Version: 2}) {
		t.Fatalf("Get = %+v, %v; stale write must not apply", got, err)
	}
}

func testCASSingleWinner(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	if _, err := s.CreateSetting(ctx, "race", "init"); err != nil {
		t.Fatal(err)
	}
	const n = 8
	var won, stale atomic.Int32
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.UpdateSetting(ctx, "race", "v", 1)
			switch {
			case err == nil:
				won.Add(1)
			case errors.Is(err, store.ErrStale):
				stale.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 || stale.Load() != n-1 {
		t.Fatalf("winners=%d stale=%d; want 1 and %d", won.Load(), stale.Load(), n-1)
	}
	if got, _ := s.GetSetting(ctx, "race"); got.Version != 2 {
		t.Fatalf("version = %d; want 2", got.Version)
	}
}
