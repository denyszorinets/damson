//go:build integration

package logs

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/denyszorinets/damson/internal/store"
)

var (
	pgOnce      sync.Once
	pgAdminDSN  string
	pgErr       error
	pgContainer *tcpostgres.PostgresContainer
	pgDBSeq     atomic.Int64
)

// startPostgres uses DAMSON_TEST_POSTGRES_DSN or starts a testcontainers
// Postgres once, the same way internal/store/postgres tests do.
func startPostgres() {
	if dsn := os.Getenv("DAMSON_TEST_POSTGRES_DSN"); dsn != "" {
		pgAdminDSN = dsn
		return
	}
	ctx := context.Background()
	pgContainer, pgErr = tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("damson"),
		tcpostgres.WithPassword("damson"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if pgErr != nil {
		return
	}
	pgAdminDSN, pgErr = pgContainer.ConnectionString(ctx, "sslmode=disable")
}

func stopPostgres() {
	if pgContainer != nil {
		_ = testcontainers.TerminateContainer(pgContainer)
	}
}

func postgresBackends() []backend {
	return []backend{{name: "postgres", open: func(t *testing.T) store.Store { return openPostgres(t, newPGDatabase(t)) }}}
}

// newPGDatabase creates an empty database, dropped on cleanup, and returns its DSN.
func newPGDatabase(t *testing.T) string {
	t.Helper()
	pgOnce.Do(startPostgres)
	if pgErr != nil {
		if os.Getenv("DAMSON_SKIP_POSTGRES") == "1" {
			t.Skipf("SKIP: postgres log ingest tests (DAMSON_SKIP_POSTGRES=1): %v", pgErr)
		}
		t.Fatalf("postgres log ingest tests need Docker or DAMSON_TEST_POSTGRES_DSN (set DAMSON_SKIP_POSTGRES=1 to skip): %v", pgErr)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgAdminDSN)
	if err != nil {
		t.Fatalf("connecting admin: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	name := fmt.Sprintf("lg_%d_%d", os.Getpid(), pgDBSeq.Add(1))
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("creating database: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, pgAdminDSN)
		if err != nil {
			t.Logf("dropping %s: %v", name, err)
			return
		}
		defer func() { _ = c.Close(ctx) }()
		if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("dropping %s: %v", name, err)
		}
	})
	u, err := url.Parse(pgAdminDSN)
	if err != nil {
		t.Fatalf("parsing admin DSN: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}
