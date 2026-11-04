package dispatch_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/secrets"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
	"github.com/denyszorinets/damson/internal/store"
)

func newSecretBackend(t *testing.T, st store.Store) *secretsdb.Backend {
	t.Helper()
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	ring, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(k), "")
	if err != nil {
		t.Fatal(err)
	}
	return secretsdb.New(st, ring, rand.Reader, time.Now, uuid.NewString)
}

func putSecret(t *testing.T, b *secretsdb.Backend, name string, fields map[string]string) {
	t.Helper()
	if _, _, err := b.Put(context.Background(), "dev", name, "generic", fields, nil); err != nil {
		t.Fatal(err)
	}
}

// A secret stored by the db backend reaches the agent, is masked in the stored
// log, and its value shows up in no controller log, step input or error.
func TestDBSecretDeliveredAndMasked(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		b := newSecretBackend(t, st)
		putSecret(t, b, "token", map[string]string{"value": secretMarker})
		putSecret(t, b, "cred", map[string]string{"username": "damson", "private_key": secretMarker + "-KEY"})
		logs := &lockedBuf{}
		log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		src := &fakeSources{}
		c := startClusterLog(t, st, nodeSpec, log, func(c *dispatch.Config) {
			c.Nodes, c.Secrets = src, node.NewSecretSource(b, log)
		})
		id := c.triggerInput("nodedemo", `{"node":"n1","token":"secret://dev/token"}`)
		tree := c.waitRun(id, store.RunSucceeded)
		waitLog(t, st, tree.Steps[0].Executions[0].ID, "node 10.0.0.7 user *** token-len "+strconv.Itoa(len(secretMarker)))
		if strings.Contains(logs.String(), secretMarker) {
			t.Fatalf("a secret value reached the controller log: %s", logs.String())
		}
		n := tree.Steps[0]
		if strings.Contains(string(n.Input), secretMarker) || strings.Contains(n.Error, secretMarker) || strings.Contains(n.Executions[0].Error, secretMarker) {
			t.Fatal("a secret value was stored")
		}
	})
}

// A missing secret and an undecryptable one both fail the execution for good,
// naming only the ref.
func TestDBSecretMissingOrUnreadableFails(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		other := newSecretBackend(t, st) // another KEK: its rows cannot be read by the dispatcher's
		if _, _, err := other.Put(context.Background(), "dev", "token", "generic", map[string]string{"value": secretMarker}, nil); err != nil {
			t.Fatal(err)
		}
		logs := &lockedBuf{}
		log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		reader := newSecretBackend(t, st)
		putSecret(t, reader, "cred", map[string]string{"username": "damson", "private_key": secretMarker + "-KEY"})
		c := startClusterLog(t, st, nodeSpec, log, func(c *dispatch.Config) {
			c.Nodes, c.Secrets = &fakeSources{}, node.NewSecretSource(reader, log)
		})
		for _, ref := range []string{"secret://dev/token", "secret://dev/nope", "secret://Bad/x"} {
			tree := c.waitRun(c.triggerInput("nodedemo", `{"node":"n1","token":"`+ref+`"}`), store.RunFailed)
			ex := tree.Steps[0].Executions[0]
			if ex.State != store.ExecFailed || !strings.Contains(ex.Error, ref) || strings.Contains(ex.Error, secretMarker) {
				t.Fatalf("%s: execution = %s %q", ref, ex.State, ex.Error)
			}
		}
		if strings.Contains(logs.String(), secretMarker) {
			t.Fatalf("a secret value reached the controller log: %s", logs.String())
		}
	})
}
