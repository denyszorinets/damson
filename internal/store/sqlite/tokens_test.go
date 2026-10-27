package sqlite_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store/sqlite"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

// TestPoolTokenPlaintextNeverPersisted proves only the hash reaches the file.
func TestPoolTokenPlaintextNeverPersisted(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "damson.db")
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := s.CreatePool(ctx, "p", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	plain, hash, err := tokens.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePoolToken(ctx, pool.ID, "ci", hash, time.Now()); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var stored string
	if err := db.QueryRow("SELECT token_hash FROM pool_tokens").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != hash || stored == plain {
		t.Fatalf("stored %q; want the hash only", stored)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{path, path + "-wal"} {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), plain) {
			t.Fatalf("plaintext token found in %s", f)
		}
	}
}
