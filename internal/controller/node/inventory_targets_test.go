package node_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/store"
)

func addTarget(t *testing.T, st store.Store, name, osName string, enabled bool) {
	t.Helper()
	if _, err := st.CreateNode(context.Background(), store.Node{
		ID: "id-" + name, Name: name, Address: name + ".example.com", Port: 22, Transport: "ssh", OS: osName, Pool: "p",
		Tags: []string{"prod"}, Attributes: map[string]any{"role": "web"}, Credential: "secret://_inventory/k",
		HostKey: "ssh-ed25519 AAAA", HostKeyPolicy: "strict", Source: "manual", Enabled: enabled,
	}, t0); err != nil {
		t.Fatal(err)
	}
}

type onlyScope map[string]bool

func (o onlyScope) Allows(_ context.Context, _ string, n store.Node) (bool, error) {
	return o[n.Name], nil
}

func TestInventoryTargets(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	addTarget(t, st, "web-02", "linux", true)
	addTarget(t, st, "web-01", "linux", true)
	addTarget(t, st, "off", "linux", false)
	addTarget(t, st, "win-01", "windows", true)

	got, err := (&node.InventoryTargets{Store: st, Scope: inventory.AllNodes{}}).Targets(ctx, "default", nil, 1000)
	if err != nil || len(got) != 3 {
		t.Fatalf("Targets = %v, %v; want the three enabled nodes", got, err)
	}
	for i, want := range []string{"web-01", "web-02", "win-01"} {
		if got[i]["name"] != want {
			t.Fatalf("order[%d] = %v, want %s", i, got[i]["name"], want)
		}
	}
	m := got[0]
	if m["address"] != "web-01.example.com" || m["os"] != "linux" || m["pool"] != "p" || m["transport"] != "ssh" ||
		m["attributes"].(map[string]any)["role"] != "web" || m["tags"].([]any)[0] != "prod" {
		t.Fatalf("target map = %v", m)
	}
	for _, key := range []string{"credential", "host_key", "host_key_policy", "port"} {
		if _, ok := m[key]; ok {
			t.Errorf("target map carries %q", key)
		}
	}

	// The project's scope filters the list.
	scoped, err := (&node.InventoryTargets{Store: st, Scope: onlyScope{"web-02": true}}).Targets(ctx, "default", nil, 1000)
	if err != nil || len(scoped) != 1 || scoped[0]["name"] != "web-02" {
		t.Fatalf("scoped = %v, %v", scoped, err)
	}

	// A database error is an error, not an empty list.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (&node.InventoryTargets{Store: st, Scope: inventory.AllNodes{}}).Targets(ctx, "default", nil, 1000); err == nil {
		t.Fatal("a failing store returned no error")
	}
}

type failingScope struct{}

func (failingScope) Allows(context.Context, string, store.Node) (bool, error) {
	return false, errors.New("scope backend down")
}

func TestInventoryTargetsScopeError(t *testing.T) {
	st := newStore(t)
	addTarget(t, st, "a", "linux", true)
	if _, err := (&node.InventoryTargets{Store: st, Scope: failingScope{}}).Targets(context.Background(), "p", nil, 1000); err == nil {
		t.Fatal("a failing scope returned no error")
	}
}

// The selector sees the whole inventory, not its first 1000 names, and an
// overflow is visible (limit+1 picks).
func TestInventoryTargetsScansEverything(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	for i := 0; i < 1100; i++ {
		os := "linux"
		if i >= 1050 {
			os = "windows"
		}
		addTarget(t, st, fmt.Sprintf("n%04d", i), os, true)
	}
	src := &node.InventoryTargets{Store: st, Scope: inventory.AllNodes{}}
	only := func(want string) func([]map[string]any) []map[string]any {
		return func(page []map[string]any) []map[string]any {
			var out []map[string]any
			for _, n := range page {
				if n["os"] == want {
					out = append(out, n)
				}
			}
			return out
		}
	}
	got, err := src.Targets(ctx, "default", only("windows"), 1001)
	if err != nil || len(got) != 50 || got[0]["name"] != "n1050" {
		t.Fatalf("selector past the 1000th name: %d nodes, %v", len(got), err)
	}
	got, err = src.Targets(ctx, "default", only("linux"), 1001)
	if err != nil || len(got) != 1001 {
		t.Fatalf("1050 matches with limit 1001 returned %d, %v", len(got), err)
	}
}
