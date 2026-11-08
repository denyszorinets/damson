package openbao_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/secrets/openbao"
	"github.com/denyszorinets/damson/internal/secrets/openbao/baotest"
	"github.com/denyszorinets/damson/internal/secrets/secretstest"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite"
)

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func newStore(t *testing.T) store.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "damson.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

// newBackend starts a client against srv with the root token.
func newBackend(t *testing.T, srv *baotest.Server, st store.Store, mod func(*openbao.Config)) *openbao.Backend {
	t.Helper()
	cfg := openbao.Config{
		Address: srv.URL(), AllowPlainHTTP: true, Prefix: "damson",
		Auth: openbao.Auth{Method: openbao.AuthToken, Token: baotest.RootToken},
	}
	if mod != nil {
		mod(&cfg)
	}
	c, err := openbao.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b := openbao.NewBackend(c, st, func() time.Time { return t0 })
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })
	return b
}

func TestContract(t *testing.T) {
	secretstest.Run(t, func(t *testing.T) secretstest.Fixture {
		st := newStore(t)
		return secretstest.Fixture{Backend: newBackend(t, baotest.Start(t), st, nil), Audit: st}
	})
}
