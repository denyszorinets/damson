//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres"
	"github.com/denyszorinets/damson/internal/store/storetest"
)

// adminDSN is the server every test database is created on. It comes from
// DAMSON_TEST_POSTGRES_DSN, or from a testcontainers Postgres started once.
var adminDSN string

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if dsn := os.Getenv("DAMSON_TEST_POSTGRES_DSN"); dsn != "" {
		adminDSN = dsn
		return m.Run()
	}
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("damson"),
		tcpostgres.WithPassword("damson"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		if os.Getenv("DAMSON_SKIP_POSTGRES") == "1" {
			fmt.Fprintln(os.Stderr, "SKIP: postgres store tests (DAMSON_SKIP_POSTGRES=1):", err)
			return 0 // explicit opt-out; Postgres is NOT verified
		}
		fmt.Fprintln(os.Stderr, "postgres store tests need Docker or DAMSON_TEST_POSTGRES_DSN (set DAMSON_SKIP_POSTGRES=1 to skip):", err)
		return 1
	}
	defer func() { _ = testcontainers.TerminateContainer(c) }()
	if adminDSN, err = c.ConnectionString(ctx, "sslmode=disable"); err != nil {
		fmt.Fprintln(os.Stderr, "postgres connection string:", err)
		return 1
	}
	return m.Run()
}

var dbSeq atomic.Int64

// newDatabase creates an empty database, dropped on cleanup, and returns its DSN.
func newDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connecting admin: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	name := fmt.Sprintf("t_%d_%d", os.Getpid(), dbSeq.Add(1))
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("creating database: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, adminDSN)
		if err != nil {
			t.Logf("dropping %s: %v", name, err)
			return
		}
		defer func() { _ = c.Close(ctx) }()
		if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("dropping %s: %v", name, err)
		}
	})
	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parsing admin DSN: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

func TestVersionFreshDBHasNoSideEffects(t *testing.T) {
	ctx := context.Background()
	dsn := newDatabase(t)
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if v, err := s.MigrationVersion(ctx); err != nil || v != 0 {
		t.Fatalf("MigrationVersion = %d, %v; want 0, nil", v, err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_name = 'schema_migrations'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("MigrationVersion created schema_migrations")
	}
}

func TestNewDatabaseUsesOwnDatabase(t *testing.T) {
	dsn := newDatabase(t)
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var db string
	if err := conn.QueryRow(context.Background(), "SELECT current_database()").Scan(&db); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(db, "t_") {
		t.Fatalf("connected to %q, want per-test database", db)
	}
}

func TestSuite(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := postgres.Open(context.Background(), newDatabase(t))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestOpenErrorOmitsDSN(t *testing.T) {
	_, err := postgres.Open(context.Background(), "postgres://u:hunter2@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Fatal("Open on closed port succeeded")
	}
	if got := err.Error(); strings.Contains(got, "hunter2") {
		t.Fatalf("error leaks password: %v", err)
	}
}

// TestMigrateAcrossControllers is the real multi-controller path: several
// stores, each with its own connection pool, migrate one empty database at the
// same time. The advisory locks serialize them; all must succeed and leave the
// schema (goose and River) fully applied.
func TestMigrateAcrossControllers(t *testing.T) {
	ctx := context.Background()
	dsn := newDatabase(t)
	const controllers = 6
	stores := make([]*postgres.Store, controllers)
	for i := range stores {
		s, err := postgres.Open(ctx, dsn)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		t.Cleanup(func() { _ = s.Close() })
		stores[i] = s
	}
	errs := make(chan error, controllers)
	start := make(chan struct{})
	for _, s := range stores {
		go func() {
			<-start
			errs <- s.Migrate(ctx)
		}()
	}
	close(start)
	for range stores {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Migrate across stores: %v", err)
		}
	}
	v, err := stores[0].MigrationVersion(ctx)
	if err != nil || v == 0 {
		t.Fatalf("MigrationVersion = %d, %v; want a migrated database", v, err)
	}
	if err := stores[1].Migrate(ctx); err != nil {
		t.Fatalf("Migrate again must be a no-op: %v", err)
	}
}
