package dispatch_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/inventory"
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

func putIn(t *testing.T, b *secretsdb.Backend, scope, name string, fields map[string]string) {
	t.Helper()
	if _, _, err := b.Put(context.Background(), scope, name, "generic", fields, nil); err != nil {
		t.Fatal(err)
	}
}

// Refs are authorized against the run's project: own scope and ancestors pass,
// siblings and other projects do not, _inventory only as a node credential.
// A denied ref reads as unknown, names only the ref, and no secret is read.
func TestDBSecretAuthorization(t *testing.T) {
	kv := map[string]string{"value": secretMarker, "username": "u", "private_key": "k"}
	for _, tc := range []struct {
		name, ref, cred string
		ok              bool
	}{
		{"own project", "secret://default/token", "secret://default/cred", true},
		{"other project", "secret://other/token", "secret://default/cred", false},
		{"literal inventory", "secret://_inventory/cred", "secret://default/cred", false},
		{"node credential from inventory", "secret://default/token", "secret://_inventory/cred", true},
		{"node credential from another project", "secret://default/token", "secret://other/token", false},
		{"inventory as literal and credential", "secret://_inventory/cred", "secret://_inventory/cred", false},
		{"missing field", "secret://default/token#nope", "secret://default/cred", false},
		{"present field", "secret://default/token#value", "secret://default/cred", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				b := newSecretBackend(t, st)
				for _, s := range [][2]string{{"default", "token"}, {"default", "cred"}, {"other", "token"}, {"_inventory", "cred"}} {
					putIn(t, b, s[0], s[1], kv)
				}
				logs := &lockedBuf{}
				log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
				counting := &countingSource{inner: node.NewSecretSource(b, log)}
				c := startClusterLog(t, st, nodeSpec, log, func(c *dispatch.Config) {
					c.Nodes, c.Secrets = &fakeSources{credRef: tc.cred}, counting
					c.Project = func(context.Context, string) (string, error) { return "default", nil }
				})
				want := store.RunSucceeded
				if !tc.ok {
					want = store.RunFailed
				}
				tree := c.waitRun(c.triggerInput("nodedemo", `{"node":"n1","token":"`+tc.ref+`"}`), want)
				ex := tree.Steps[0].Executions[0]
				if !tc.ok && (ex.State != store.ExecFailed || strings.Contains(ex.Error, secretMarker)) {
					t.Fatalf("execution = %s %q", ex.State, ex.Error)
				}
				if strings.Contains(logs.String(), secretMarker) {
					t.Fatal("a secret value reached the controller log")
				}
				if tc.name == "other project" && counting.reads.Load() != 0 {
					t.Fatalf("a denied ref was read (%d reads)", counting.reads.Load())
				}
			})
		})
	}
}

// countingSource counts reads, to show a denied ref is never read.
type countingSource struct {
	inner dispatch.SecretSource
	reads atomic.Int32
}

func (c *countingSource) Secret(ctx context.Context, runID, ref string) (map[string]string, error) {
	c.reads.Add(1)
	return c.inner.Secret(ctx, runID, ref)
}

// A denied ref that exists and a missing ref fail with the same message shape,
// so a job cannot probe which secrets exist in other projects.
func TestDBSecretDeniedLooksLikeMissing(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		b := newSecretBackend(t, st)
		kv := map[string]string{"value": secretMarker, "username": "u", "private_key": "k"}
		putIn(t, b, "default", "cred", kv)
		putIn(t, b, "other", "token", kv)
		log := slog.New(slog.DiscardHandler)
		c := startClusterLog(t, st, nodeSpec, log, func(c *dispatch.Config) {
			c.Nodes, c.Secrets = &fakeSources{credRef: "secret://default/cred"}, node.NewSecretSource(b, log)
			c.Project = func(context.Context, string) (string, error) { return "default", nil }
		})
		errOf := func(ref string) string {
			tree := c.waitRun(c.triggerInput("nodedemo", `{"node":"n1","token":"`+ref+`"}`), store.RunFailed)
			return strings.ReplaceAll(tree.Steps[0].Executions[0].Error, ref, "REF")
		}
		denied, missing := errOf("secret://other/token"), errOf("secret://default/missing")
		if denied != missing || !strings.Contains(denied, "unknown secret") {
			t.Fatalf("denied %q differs from missing %q", denied, missing)
		}
	})
}

func addNode(t *testing.T, st store.Store, name string, enabled bool) store.Node {
	t.Helper()
	n, err := st.CreateNode(context.Background(), store.Node{
		ID: "id-" + name, Name: name, Address: "10.0.0.7", Port: 22, Transport: "ssh", OS: "linux",
		Credential: "secret://_inventory/cred", HostKeyPolicy: "strict", Source: "manual", Enabled: enabled,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func inventoryCluster(t *testing.T, st store.Store, b *secretsdb.Backend, log *slog.Logger) *cluster {
	t.Helper()
	return startClusterLog(t, st, nodeSpec, log, func(c *dispatch.Config) {
		project := func(context.Context, string) (string, error) { return "default", nil }
		c.Nodes = &node.InventoryNodes{Store: st, Scope: inventory.AllNodes{}, Project: project}
		c.Secrets, c.Project = node.NewSecretSource(b, log), project
	})
}

// A node from the inventory delivers its _inventory credential, which is
// masked in the stored log and appears in no controller log or stored input.
func TestInventoryNodeCredentialDeliveredAndMasked(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		b := newSecretBackend(t, st)
		putIn(t, b, "_inventory", "cred", map[string]string{"username": "damson", "private_key": secretMarker + "-KEY", "value": secretMarker})
		putIn(t, b, "default", "token", map[string]string{"value": secretMarker})
		addNode(t, st, "n1", true)
		logs := &lockedBuf{}
		log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		c := inventoryCluster(t, st, b, log)
		tree := c.waitRun(c.triggerInput("nodedemo", `{"node":"n1","token":"secret://default/token"}`), store.RunSucceeded)
		waitLog(t, st, tree.Steps[0].Executions[0].ID, "node 10.0.0.7 user *** token-len "+strconv.Itoa(len(secretMarker)))
		if strings.Contains(logs.String(), secretMarker) {
			t.Fatalf("a secret value reached the controller log: %s", logs.String())
		}
		if strings.Contains(string(tree.Steps[0].Input), secretMarker) {
			t.Fatal("a secret value was stored")
		}
	})
}

// A node that is deleted or disabled fails the step for good, naming the node.
func TestInventoryNodeGoneOrDisabledFails(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		remove  bool
		want    string
	}{
		{"deleted", true, true, "unknown node"},
		{"disabled", false, false, "node is disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				b := newSecretBackend(t, st)
				putIn(t, b, "_inventory", "cred", map[string]string{"username": "u", "private_key": "k"})
				putIn(t, b, "default", "token", map[string]string{"value": "v"})
				n := addNode(t, st, "n1", tc.enabled)
				if tc.remove {
					if err := st.DeleteNode(context.Background(), n.ID, n.Version); err != nil {
						t.Fatal(err)
					}
				}
				c := inventoryCluster(t, st, b, slog.New(slog.DiscardHandler))
				tree := c.waitRun(c.triggerInput("nodedemo", `{"node":"n1","token":"secret://default/token"}`), store.RunFailed)
				ex := tree.Steps[0].Executions[0]
				if ex.State != store.ExecFailed || !strings.Contains(ex.Error, tc.want) || !strings.Contains(ex.Error, "n1") {
					t.Fatalf("execution = %s %q; want failed with %q naming n1", ex.State, ex.Error, tc.want)
				}
			})
		})
	}
}
