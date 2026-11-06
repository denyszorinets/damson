package node_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func newStore(t *testing.T) store.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func put(t *testing.T, s store.Store, name string, enabled bool) store.Node {
	t.Helper()
	n, err := s.CreateNode(context.Background(), store.Node{
		ID: "id-" + name, Name: name, Address: "10.0.0.1", Port: 22, Transport: "ssh", OS: "linux",
		Attributes: map[string]any{"env": "prod"}, Credential: "secret://_inventory/k", HostKey: "ssh-ed25519 AAAA",
		HostKeyPolicy: "strict", Source: "manual", Enabled: enabled,
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type denyAll struct{}

func (denyAll) Allows(context.Context, string, store.Node) (bool, error) { return false, nil }

func source(s store.Store, scope inventory.Scope, project func(context.Context, string) (string, error)) *node.InventoryNodes {
	if project == nil {
		project = func(context.Context, string) (string, error) { return "default", nil }
	}
	return &node.InventoryNodes{Store: s, Scope: scope, Project: project}
}

func TestNodeSource(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	put(t, s, "web-1", true)
	put(t, s, "off", false)

	info, err := source(s, inventory.AllNodes{}, nil).Node(ctx, "run", "web-1")
	if err != nil || info.Name != "web-1" || info.Address != "10.0.0.1" || info.Port != 22 || info.Credential != "secret://_inventory/k" ||
		info.HostKeyPolicy != "strict" || info.HostKey == "" || info.Attributes["env"] != "prod" {
		t.Fatalf("Node = %+v, %v", info, err)
	}
	if _, err := source(s, inventory.AllNodes{}, nil).Node(ctx, "run", "nope"); !errors.Is(err, dispatch.ErrUnknownNode) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := source(s, inventory.AllNodes{}, nil).Node(ctx, "run", "off"); !errors.Is(err, dispatch.ErrNodeDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	// Out of scope reads as unknown, also for a disabled node (no probing).
	for _, name := range []string{"web-1", "off"} {
		if _, err := source(s, denyAll{}, nil).Node(ctx, "run", name); !errors.Is(err, dispatch.ErrUnknownNode) {
			t.Fatalf("out of scope %s: %v", name, err)
		}
	}
	// A project lookup failure is transient: neither permanent error.
	_, err = source(s, inventory.AllNodes{}, func(context.Context, string) (string, error) { return "", errors.New("db down") }).Node(ctx, "run", "web-1")
	if err == nil || errors.Is(err, dispatch.ErrUnknownNode) || errors.Is(err, dispatch.ErrNodeDisabled) {
		t.Fatalf("project error = %v; want a transient error", err)
	}
	// A node deleted after the run started is unknown at dispatch.
	n, _ := s.GetNode(ctx, "web-1")
	if err := s.DeleteNode(ctx, n.ID, n.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := source(s, inventory.AllNodes{}, nil).Node(ctx, "run", "web-1"); !errors.Is(err, dispatch.ErrUnknownNode) {
		t.Fatalf("deleted: %v", err)
	}
}

// Until host key handling exists (#118) a node whose policy cannot be honored
// is refused: insecure, and tofu without a pinned key.
func TestNodeSourceHostKeyPolicies(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for i, tc := range []struct {
		name, policy, key string
		ok                bool
	}{
		{"strict-pinned", "strict", "ssh-ed25519 AAAA", true},
		{"tofu-pinned", "tofu", "ssh-ed25519 AAAA", true},
		{"tofu-unpinned", "tofu", "", false},
		{"insecure", "insecure", "", false},
	} {
		if _, err := s.CreateNode(ctx, store.Node{
			ID: fmt.Sprint(i), Name: tc.name, Address: "10.0.0.1", Port: 22, Transport: "ssh", HostKey: tc.key,
			HostKeyPolicy: tc.policy, Source: "manual", Enabled: true,
		}, t0); err != nil {
			t.Fatal(err)
		}
		_, err := source(s, inventory.AllNodes{}, nil).Node(ctx, "run", tc.name)
		if tc.ok && err != nil || !tc.ok && !errors.Is(err, dispatch.ErrHostKeyPolicy) {
			t.Errorf("%s: err = %v, ok want %v", tc.name, err, tc.ok)
		}
	}
}

// A node frozen into the run's targets is followed by id: after a rename the
// step's name still reaches it; after a delete it is unknown. A name outside
// the snapshot falls back to the name lookup.
func TestNodeSourceFollowsFrozenID(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	n := put(t, s, "web-1", true)
	put(t, s, "extra", true)
	job, ver, err := s.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "j", Spec: []byte(`{}`), Checksum: "c"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := s.CreateRun(ctx, store.NewRun{
		JobID: job.ID, JobVersionID: ver.ID,
		Targets: []store.RunTarget{{Name: "web-1", NodeID: n.ID, Node: []byte(`{"name":"web-1"}`)}},
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	renamed := n
	renamed.Name = "web-renamed"
	if err := s.ApplyNodeDiff(ctx, store.NodeDiff{Update: []store.NodeUpdate{{Node: renamed, Expected: n.Version}}}, t0); err != nil {
		t.Fatal(err)
	}
	info, err := source(s, inventory.AllNodes{}, nil).Node(ctx, tree.ID, "web-1")
	if err != nil || info.Name != "web-1" || info.Address != "10.0.0.1" {
		t.Fatalf("renamed node = %+v, %v", info, err)
	}
	if _, err := source(s, inventory.AllNodes{}, nil).Node(ctx, tree.ID, "extra"); err != nil {
		t.Fatalf("name outside the snapshot: %v", err)
	}
	cur, _ := s.GetNodeByID(ctx, n.ID)
	if err := s.DeleteNode(ctx, cur.ID, cur.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := source(s, inventory.AllNodes{}, nil).Node(ctx, tree.ID, "web-1"); !errors.Is(err, dispatch.ErrUnknownNode) {
		t.Fatalf("deleted: %v", err)
	}
}

type nameScope struct{ deny string }

func (n nameScope) Allows(_ context.Context, _ string, node store.Node) (bool, error) {
	return node.Name != n.deny, nil
}

// The scope sees the node as it is now, not under the name the run froze.
func TestNodeSourceScopeSeesCurrentName(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	n := put(t, s, "web-1", true)
	job, ver, err := s.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "j", Spec: []byte(`{}`), Checksum: "c"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := s.CreateRun(ctx, store.NewRun{JobID: job.ID, JobVersionID: ver.ID,
		Targets: []store.RunTarget{{Name: "web-1", NodeID: n.ID, Node: []byte(`{}`)}}}, t0)
	if err != nil {
		t.Fatal(err)
	}
	renamed := n
	renamed.Name = "secret-box"
	if err := s.ApplyNodeDiff(ctx, store.NodeDiff{Update: []store.NodeUpdate{{Node: renamed, Expected: n.Version}}}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := source(s, nameScope{deny: "secret-box"}, nil).Node(ctx, tree.ID, "web-1"); !errors.Is(err, dispatch.ErrUnknownNode) {
		t.Fatalf("renamed into a denied name: %v; want ErrUnknownNode", err)
	}
}
