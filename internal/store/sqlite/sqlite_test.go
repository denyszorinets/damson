package sqlite_test

import (
	"context"
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
