//go:build integration

package postgres_test

import (
	"context"
	"fmt"
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
		fmt.Fprintln(os.Stderr, "SKIP: postgres store tests: no container runtime or DAMSON_TEST_POSTGRES_DSN:", err)
		return 0 // skip cleanly; the report must say Postgres was not verified
	}
	defer func() { _ = testcontainers.TerminateContainer(c) }()
	if adminDSN, err = c.ConnectionString(ctx, "sslmode=disable"); err != nil {
		fmt.Fprintln(os.Stderr, "postgres connection string:", err)
		return 1
	}
	return m.Run()
}

var dbSeq atomic.Int64

// newDatabase creates an empty database and returns its DSN.
func newDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connecting admin: %v", err)
	}
	defer conn.Close(ctx)
	name := fmt.Sprintf("t_%d_%d", os.Getpid(), dbSeq.Add(1))
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("creating database: %v", err)
	}
	cfg := conn.Config().Copy()
	cfg.Database = name
	return cfg.ConnString()
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
