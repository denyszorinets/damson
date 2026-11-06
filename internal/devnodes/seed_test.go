package devnodes

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/secrets"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func newStores(t *testing.T) (store.Store, *secretsdb.Backend) {
	t.Helper()
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	ring, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(k), "")
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	return st, secretsdb.New(st, ring, rand.Reader, time.Now, func() string { return fmt.Sprintf("s%d", n.Add(1)) })
}

func loadFile(t *testing.T, dir, key string) *File {
	t.Helper()
	f, err := Load(write(t, dir, "nodes.json", fileJSON(key), 0o600))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func version(t *testing.T, b *secretsdb.Backend, scope, name string) int64 {
	t.Helper()
	m, err := b.Meta(context.Background(), scope, name)
	if err != nil {
		t.Fatal(err)
	}
	return m.CurrentVersion
}

func TestSeedIsIdempotentAndFollowsTheFiles(t *testing.T) {
	ctx := context.Background()
	st, b := newStores(t)
	dir := t.TempDir()
	key := write(t, dir, "key", marker, 0o600)
	f := loadFile(t, dir, key)

	rep, err := Seed(ctx, st, b, f, t0)
	if err != nil || len(rep.Nodes.Created) != 1 || len(rep.SecretsWritten) != 2 {
		t.Fatalf("first seed = %+v, %v", rep, err)
	}
	n, err := st.GetNode(ctx, "sshd")
	if err != nil || n.Source != Source || n.Version != 1 {
		t.Fatalf("node = %+v, %v", n, err)
	}
	got, err := b.Get(ctx, "_inventory", "sshd")
	if err != nil || got["private_key"] != marker || got["username"] != "damson" {
		t.Fatalf("secret = %v, %v", got, err)
	}

	// Seeding again changes nothing: no node version, no secret version.
	rep, err = Seed(ctx, st, b, f, t0)
	if err != nil || len(rep.Nodes.Unchanged) != 1 || len(rep.SecretsWritten) != 0 || len(rep.SecretsUnchanged) != 2 {
		t.Fatalf("second seed = %+v, %v", rep, err)
	}
	if n, _ := st.GetNode(ctx, "sshd"); n.Version != 1 {
		t.Fatalf("node version = %d", n.Version)
	}
	if version(t, b, "_inventory", "sshd") != 1 || version(t, b, "default", "token") != 1 {
		t.Fatal("a re-seed added a secret version")
	}

	// The first seed wrote one create event per secret and the re-seed none.
	evs, err := st.ListAuditEvents(ctx, store.AuditFilter{TargetType: store.AuditTargetSecret}, 20)
	if err != nil || len(evs) != 2 {
		t.Fatalf("events after two seeds = %+v, %v", evs, err)
	}
	for _, e := range evs {
		if e.Actor != SeederActor || e.Action != store.ActionSecretCreate || strings.Contains(string(e.Details), marker) {
			t.Fatalf("event = %+v", e)
		}
	}

	// A changed key file gives exactly one new version of the secrets that use it.
	write(t, dir, "key", "rotated", 0o600)
	rep, err = Seed(ctx, st, b, f, t0)
	if err != nil || len(rep.SecretsWritten) != 2 {
		t.Fatalf("seed after rotation = %+v, %v", rep, err)
	}
	if version(t, b, "_inventory", "sshd") != 2 {
		t.Fatalf("version after rotation = %d, want 2", version(t, b, "_inventory", "sshd"))
	}
	if got, _ := b.Get(ctx, "_inventory", "sshd"); got["private_key"] != "rotated" {
		t.Fatalf("rotated key = %q", got["private_key"])
	}
}

func TestSeedPrunesOnlyDevfileNodes(t *testing.T) {
	ctx := context.Background()
	st, b := newStores(t)
	dir := t.TempDir()
	key := write(t, dir, "key", marker, 0o600)
	for _, n := range []store.Node{
		{ID: "m", Name: "manual-1", Address: "10.0.0.1", Port: 22, Transport: "ssh", HostKeyPolicy: "strict", Source: "manual", Enabled: true},
		{ID: "o", Name: "other-1", ExternalID: "x", Address: "10.0.0.2", Port: 22, Transport: "ssh", HostKeyPolicy: "strict", Source: "sync1", Enabled: true},
		{ID: "d", Name: "old-dev", ExternalID: "old-dev", Address: "10.0.0.3", Port: 22, Transport: "ssh", HostKeyPolicy: "strict", Source: Source, Enabled: true},
	} {
		if _, err := st.CreateNode(ctx, n, t0); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := Seed(ctx, st, b, loadFile(t, dir, key), t0)
	if err != nil || len(rep.Nodes.Deleted) != 1 || rep.Nodes.Deleted[0] != "old-dev" {
		t.Fatalf("seed = %+v, %v", rep, err)
	}
	for _, name := range []string{"manual-1", "other-1", "sshd"} {
		if _, err := st.GetNode(ctx, name); err != nil {
			t.Errorf("node %s: %v", name, err)
		}
	}
}

func TestSeedFailures(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	key := write(t, dir, "key", marker, 0o600)

	t.Run("a manual node with the same name", func(t *testing.T) {
		st, b := newStores(t)
		if _, err := st.CreateNode(ctx, store.Node{ID: "m", Name: "sshd", Address: "10.0.0.1", Port: 22, Transport: "ssh", HostKeyPolicy: "strict", Source: "manual", Enabled: true}, t0); err != nil {
			t.Fatal(err)
		}
		_, err := Seed(ctx, st, b, loadFile(t, dir, key), t0)
		if !errors.Is(err, inventory.ErrSyncConflict) || !strings.Contains(err.Error(), `"manual"`) {
			t.Fatalf("err = %v; want a conflict naming the owner", err)
		}
	})
	t.Run("type mismatch names the ref", func(t *testing.T) {
		st, b := newStores(t)
		if _, _, err := b.Put(store.WithAudit(ctx, store.AuditMeta{Actor: "test"}), "default", "token", "generic", map[string]string{"token": "x"}, nil); err != nil {
			t.Fatal(err)
		}
		_, err := Seed(ctx, st, b, loadFile(t, dir, key), t0)
		if err == nil || !strings.Contains(err.Error(), "secret://default/token") || strings.Contains(err.Error(), marker) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an open key file is refused without leaking", func(t *testing.T) {
		st, b := newStores(t)
		open := write(t, dir, "openkey", marker, 0o644)
		_, err := Seed(ctx, st, b, loadFile(t, dir, open), t0)
		if err == nil || !strings.Contains(err.Error(), "accessible by group or others") || strings.Contains(err.Error(), marker) {
			t.Fatalf("err = %v", err)
		}
		if _, err := st.GetNode(ctx, "sshd"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("nodes were seeded although a secret failed")
		}
	})
}

func TestSeedWarnings(t *testing.T) {
	dir := t.TempDir()
	key := write(t, dir, "key", marker, 0o600)
	f, err := Load(write(t, dir, "w.json", `{"version":1,
"nodes":{"loose":{"address":"10.0.0.1","host_key_policy":"insecure"},"tofu":{"address":"10.0.0.2","host_key_policy":"tofu"},
"pinned":{"address":"10.0.0.3","host_key_policy":"tofu","host_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"}},
"secrets":{"secret://_inventory/unused":{"username":"u","k_file":"`+key+`"}}}`, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(f.warnings(), "\n")
	for _, want := range []string{`node "loose"`, `node "tofu"`, "secret://_inventory/unused"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings %q lack %q", w, want)
		}
	}
	if strings.Contains(w, `node "pinned"`) {
		t.Errorf("a pinned tofu node was warned about: %s", w)
	}
}

// scriptedWriter is a SecretWriter that plays a concurrent seeder: Put can fail
// with a version conflict a set number of times, and the stored secret can
// appear or change between calls.
type scriptedWriter struct {
	exists     bool
	typ        string
	version    int64
	fields     map[string]string
	conflicts  int // Put calls that fail with ErrVersionConflict before one succeeds
	onConflict func(w *scriptedWriter)
	puts       []int64 // the ifVersion of each Put
	getGone    int     // Get calls that report the secret deleted
}

func (w *scriptedWriter) Meta(context.Context, string, string) (store.SecretMeta, error) {
	if !w.exists {
		return store.SecretMeta{}, secrets.ErrNotFound
	}
	return store.SecretMeta{Type: w.typ, CurrentVersion: w.version}, nil
}

func (w *scriptedWriter) Get(context.Context, string, string) (map[string]string, error) {
	if w.getGone > 0 {
		w.getGone--
		return nil, secrets.ErrNotFound
	}
	return maps.Clone(w.fields), nil
}

func (w *scriptedWriter) Put(_ context.Context, _, _, typ string, fields map[string]string, ifVersion *int64) (store.SecretMeta, bool, error) {
	w.puts = append(w.puts, *ifVersion)
	if w.conflicts > 0 {
		w.conflicts--
		if w.onConflict != nil {
			w.onConflict(w)
		}
		return store.SecretMeta{}, false, secrets.ErrVersionConflict
	}
	w.exists, w.typ, w.fields, w.version = true, typ, maps.Clone(fields), w.version+1
	return store.SecretMeta{}, !w.exists, nil
}

func TestSeedSecretAgainstAConcurrentSeeder(t *testing.T) {
	ctx := context.Background()
	def := secretDef{ref: secrets.Ref{Scope: "default", Name: "tok"}, typ: "generic", literals: map[string]string{"username": "u"}, files: map[string]string{}}
	want := map[string]string{"username": "u"}

	t.Run("another seeder created it with the same value", func(t *testing.T) {
		w := &scriptedWriter{conflicts: 1, onConflict: func(w *scriptedWriter) {
			w.exists, w.typ, w.version, w.fields = true, "generic", 1, maps.Clone(want)
		}}
		wrote, err := seedSecret(ctx, w, def)
		if err != nil || wrote || len(w.puts) != 1 || w.puts[0] != 0 {
			t.Fatalf("wrote %v, err %v, puts %v; want unchanged after one create-only attempt", wrote, err, w.puts)
		}
	})
	t.Run("another seeder created it with another value", func(t *testing.T) {
		w := &scriptedWriter{conflicts: 1, onConflict: func(w *scriptedWriter) {
			w.exists, w.typ, w.version, w.fields = true, "generic", 3, map[string]string{"username": "someone-else"}
		}}
		wrote, err := seedSecret(ctx, w, def)
		if err != nil || !wrote || len(w.puts) != 2 || w.puts[0] != 0 || w.puts[1] != 3 {
			t.Fatalf("wrote %v, err %v, puts %v; want a retry against version 3", wrote, err, w.puts)
		}
	})
	t.Run("conflicts every time", func(t *testing.T) {
		w := &scriptedWriter{conflicts: 100}
		if _, err := seedSecret(ctx, w, def); err == nil || !strings.Contains(err.Error(), "changed concurrently") || len(w.puts) != maxSecretAttempts {
			t.Fatalf("err %v after %d puts; want %d attempts then an error", err, len(w.puts), maxSecretAttempts)
		}
	})
	t.Run("deleted between the two reads", func(t *testing.T) {
		w := &scriptedWriter{exists: true, typ: "generic", version: 2, fields: map[string]string{"username": "old"}, getGone: 1}
		wrote, err := seedSecret(ctx, w, def)
		if err != nil || !wrote {
			t.Fatalf("wrote %v, err %v; want a re-read and a write", wrote, err)
		}
	})
	t.Run("equal values are not written", func(t *testing.T) {
		w := &scriptedWriter{exists: true, typ: "generic", version: 2, fields: maps.Clone(want)}
		if wrote, err := seedSecret(ctx, w, def); err != nil || wrote || len(w.puts) != 0 {
			t.Fatalf("wrote %v, err %v, puts %v", wrote, err, w.puts)
		}
	})
}
