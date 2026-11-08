package dispatch_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/secrets/openbao"
	"github.com/denyszorinets/damson/internal/secrets/openbao/baotest"
	"github.com/denyszorinets/damson/internal/store"
)

func newOpenBaoBackend(t *testing.T, st store.Store, srv *baotest.Server, cache time.Duration) *openbao.Backend {
	t.Helper()
	c, err := openbao.New(openbao.Config{
		Address: srv.URL(), AllowPlainHTTP: true, Prefix: "damson", CacheTTL: cache,
		Auth: openbao.Auth{Method: openbao.AuthToken, Token: baotest.RootToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := openbao.NewBackend(c, st, time.Now)
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })
	return b
}

// A secret held in OpenBao reaches the agent, is masked in the stored log, and
// its value, like the OpenBao token, shows up in no controller log, stored step
// input, error or audit event (CLAUDE.md rule 5).
func TestOpenBaoSecretDeliveredAndMasked(t *testing.T) {
	for _, cache := range []time.Duration{0, 30 * time.Second} {
		t.Run("cache="+cache.String(), func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				srv := baotest.Start(t)
				b := newOpenBaoBackend(t, st, srv, cache)
				ctx := store.WithAudit(context.Background(), store.AuditMeta{Actor: "test"})
				if _, _, err := b.Put(ctx, "dev", "token", "generic", map[string]string{"value": secretMarker}, nil); err != nil {
					t.Fatal(err)
				}
				if _, _, err := b.Put(ctx, "dev", "cred", "ssh_key", map[string]string{"username": "damson", "private_key": secretMarker + "-KEY"}, nil); err != nil {
					t.Fatal(err)
				}
				logs := &lockedBuf{}
				log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
				c := startClusterLog(t, st, nodeSpec, log, func(c *dispatch.Config) {
					c.Nodes, c.Secrets = &fakeSources{}, node.NewSecretSource(b, log)
				})
				id := c.triggerInput("nodedemo", `{"node":"n1","token":"secret://dev/token"}`)
				tree := c.waitRun(id, store.RunSucceeded)
				waitLog(t, st, tree.Steps[0].Executions[0].ID, "node 10.0.0.7 user *** token-len "+strconv.Itoa(len(secretMarker)))
				if strings.Contains(logs.String(), secretMarker) || strings.Contains(logs.String(), baotest.RootToken) {
					t.Fatalf("a secret or the OpenBao token reached the controller log: %s", logs.String())
				}
				n := tree.Steps[0]
				if strings.Contains(string(n.Input), secretMarker) || strings.Contains(n.Error, secretMarker) || strings.Contains(n.Executions[0].Error, secretMarker) {
					t.Fatal("a secret value was stored")
				}
				evs, err := st.ListAuditEvents(context.Background(), store.AuditFilter{}, 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range evs {
					if strings.Contains(string(e.Details), secretMarker) {
						t.Fatalf("a secret value is in audit event %s: %s", e.Action, e.Details)
					}
				}
			})
		})
	}
}

// A backend failure surfaces without the secret or the token, and a transient
// one is retried by the dispatcher instead of failing the step for good.
func TestOpenBaoFailureLeaksNothing(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		srv := baotest.Start(t)
		b := newOpenBaoBackend(t, st, srv, 0)
		ctx := store.WithAudit(context.Background(), store.AuditMeta{Actor: "test"})
		if _, _, err := b.Put(ctx, "dev", "token", "generic", map[string]string{"value": secretMarker}, nil); err != nil {
			t.Fatal(err)
		}
		srv.Fail = func(r baotest.Request) int {
			if r.Method == http.MethodGet && strings.Contains(r.Path, "/data/") {
				return http.StatusInternalServerError
			}
			return 0
		}
		src := node.NewSecretSource(b, slog.New(slog.DiscardHandler))
		_, err := src.Secret(context.Background(), "", "secret://dev/token")
		if err == nil || errors.Is(err, dispatch.ErrSecretUnavailable) || errors.Is(err, dispatch.ErrUnknownSecret) {
			t.Fatalf("err = %v; want a transient error", err)
		}
		if strings.Contains(err.Error(), secretMarker) || strings.Contains(err.Error(), baotest.RootToken) {
			t.Fatalf("the error leaks: %v", err)
		}
	})
}
