package node_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/devnodes"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite"
)

func devFile(t *testing.T, withSecret bool) *devnodes.File {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("KEYBODY"), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets := ""
	if withSecret {
		secrets = `,"secrets":{"secret://_inventory/k":{"username":"u","private_key_file":"` + key + `"}}`
	}
	p := filepath.Join(dir, "nodes.json")
	body := `{"version":1,"nodes":{"dev-1":{"address":"10.0.0.1","host_key_policy":"strict","credential":"secret://_inventory/k"}}` + secrets + `}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := devnodes.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func startDev(t *testing.T, st store.Store, f *devnodes.File, kek string) (*node.Node, error) {
	t.Helper()
	reg, err := pluginreg.New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return node.Start(context.Background(), node.Config{Store: st, ControllerID: "c", Plugins: reg, InsecureDev: true, KEK: kek, DevFile: f})
}

// The dev file is seeded into the stores at start, idempotently.
func TestStartSeedsTheDevFile(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	f := devFile(t, true)
	key := kek()
	for range 2 { // the second start must change nothing
		n, err := startDev(t, st, f, key)
		if err != nil {
			t.Fatal(err)
		}
		stopNode(t, n)
	}
	got, err := st.GetNode(ctx, "dev-1")
	if err != nil || got.Source != "devfile" || got.Version != 1 {
		t.Fatalf("node = %+v, %v", got, err)
	}
}

// Secrets in the file need a secret backend; no database means no seeding.
func TestStartDevFileFailures(t *testing.T) {
	st := migratedStore(t)
	if n, err := startDev(t, st, devFile(t, true), ""); err == nil {
		stopNode(t, n)
		t.Fatal("a file with secrets started without a KEK")
	} else if !strings.Contains(err.Error(), "DAMSON_KEK") {
		t.Fatalf("err = %v; want advice to set DAMSON_KEK", err)
	}
	// Without secrets the file seeds nodes with no KEK at all.
	n, err := startDev(t, st, devFile(t, false), "")
	if err != nil {
		t.Fatal(err)
	}
	stopNode(t, n)

	unmigrated, err := openUnmigrated(t)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := startDev(t, unmigrated, devFile(t, false), ""); err == nil {
		stopNode(t, n)
		t.Fatal("started on an unmigrated database")
	} else if !strings.Contains(err.Error(), "migrated") {
		t.Fatalf("err = %v", err)
	}
}

func openUnmigrated(t *testing.T) (store.Store, error) {
	t.Helper()
	st, err := sqlite.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "u.db"))
	if err == nil {
		t.Cleanup(func() { _ = st.Close() })
	}
	return st, err
}
