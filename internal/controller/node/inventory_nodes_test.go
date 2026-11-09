package node_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
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
		{"tofu-unpinned", "tofu", "", true},
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

// claimedAttempt makes a run with one dispatched execution of agent "a1" and
// returns the run and attempt ids, with the given snapshot.
func claimedAttempt(t *testing.T, s store.Store, targets []store.RunTarget) (runID, attempt string) {
	t.Helper()
	ctx := context.Background()
	job, ver, err := s.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: fmt.Sprint("j", len(targets), time.Now().UnixNano()), Spec: []byte(`{}`), Checksum: "c"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := s.CreateRun(ctx, store.NewRun{JobID: job.ID, JobVersionID: ver.ID, Targets: targets,
		Steps: []store.NewStepInstance{{Path: "s", Plugin: "ssh", Execution: &store.NewExecution{Pool: "default", ScheduledAt: t0}}}}, t0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimExecutions(ctx, store.ClaimParams{AgentID: "a1", ControllerID: "c", Pools: []string{"default"}, Limit: 1, LeaseTTL: time.Hour}, t0)
	if err != nil || len(got) != 1 {
		t.Fatalf("claim = %v, %v", got, err)
	}
	return tree.ID, got[0].AttemptID
}

// A tofu node is held to its pin, else to the first key observed; before any
// key the agent gets none and reports the first it sees. A strict node ignores
// an observed key; scope and enabled apply to recording as to dispatch.
func TestNodeSourceTOFUKeys(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, err := s.CreateNode(ctx, store.Node{ID: "t1", Name: "tofu", Address: "h", Port: 22, Transport: "ssh",
		HostKeyPolicy: "tofu", Source: "manual", Enabled: true}, t0); err != nil {
		t.Fatal(err)
	}
	runID, attempt := claimedAttempt(t, s, nil)
	src := source(s, inventory.AllNodes{}, nil)
	if info, err := src.Node(ctx, runID, "tofu"); err != nil || info.HostKey != "" || info.HostKeyPolicy != "tofu" {
		t.Fatalf("before observation = %+v, %v", info, err)
	}
	var logs bytes.Buffer
	src.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	if err := src.ObserveHostKey(ctx, runID, attempt, "a1", "tofu", "ssh-ed25519 FIRST"); err != nil {
		t.Fatal(err)
	}
	// A different second key is not stored, and the conflict is logged.
	if err := src.ObserveHostKey(ctx, runID, attempt, "a1", "tofu", "ssh-ed25519 SECOND"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "different host key") {
		t.Fatalf("no warning for a conflicting key: %s", logs.String())
	}
	// An attempt of another agent records nothing (checked on a second node).
	if _, err := s.CreateNode(ctx, store.Node{ID: "t2", Name: "tofu-2", Address: "h", Port: 22, Transport: "ssh",
		HostKeyPolicy: "tofu", Source: "manual", Enabled: true}, t0); err != nil {
		t.Fatal(err)
	}
	if err := src.ObserveHostKey(ctx, runID, attempt, "other-agent", "tofu-2", "ssh-ed25519 X"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.GetNode(ctx, "tofu-2"); n.HostKeyObserved != "" {
		t.Fatal("a foreign agent recorded a key")
	}
	if info, _ := src.Node(ctx, runID, "tofu"); info.HostKey != "ssh-ed25519 FIRST" {
		t.Fatalf("after observation HostKey = %q; want the first key", info.HostKey)
	}
	// A pin wins over an observation.
	n, _ := s.GetNode(ctx, "tofu")
	n.HostKey = "ssh-ed25519 PINNED"
	if _, err := s.UpdateNode(ctx, n, n.Version, t0); err != nil {
		t.Fatal(err)
	}
	if info, _ := src.Node(ctx, runID, "tofu"); info.HostKey != "ssh-ed25519 PINNED" {
		t.Fatalf("pinned HostKey = %q", info.HostKey)
	}
	if err := src.ObserveHostKey(ctx, runID, attempt, "a1", "missing", "ssh-ed25519 X"); err != nil {
		t.Fatalf("observing for an unknown node = %v; want it ignored", err)
	}

	// A strict node never uses an observed key; out-of-scope and disabled nodes do not record.
	put(t, s, "strict-1", true)
	if info, _ := src.Node(ctx, runID, "strict-1"); info.HostKey != "ssh-ed25519 AAAA" {
		t.Fatalf("strict HostKey = %q", info.HostKey)
	}
	if _, err := s.CreateNode(ctx, store.Node{ID: "t3", Name: "tofu-off", Address: "h", Port: 22, Transport: "ssh",
		HostKeyPolicy: "tofu", Source: "manual", Enabled: false}, t0); err != nil {
		t.Fatal(err)
	}
	if err := src.ObserveHostKey(ctx, runID, attempt, "a1", "tofu-off", "ssh-ed25519 X"); err != nil {
		t.Fatal(err)
	}
	if err := source(s, denyAll{}, nil).ObserveHostKey(ctx, runID, attempt, "a1", "tofu-2", "ssh-ed25519 X"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tofu-off", "tofu-2"} {
		if n, _ := s.GetNode(ctx, name); n.HostKeyObserved != "" {
			t.Fatalf("%s recorded a key it should not have", name)
		}
	}
}

// A report under the name a run froze reaches the same node after a rename.
func TestObserveHostKeyFollowsFrozenID(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, err := s.CreateNode(ctx, store.Node{ID: "t1", Name: "old-name", Address: "h", Port: 22, Transport: "ssh",
		HostKeyPolicy: "tofu", Source: "manual", Enabled: true}, t0); err != nil {
		t.Fatal(err)
	}
	runID, attempt := claimedAttempt(t, s, []store.RunTarget{{Name: "old-name", NodeID: "t1", Node: []byte(`{}`)}})
	n, _ := s.GetNodeByID(ctx, "t1")
	n.Name = "new-name"
	if err := s.ApplyNodeDiff(ctx, store.NodeDiff{Update: []store.NodeUpdate{{Node: n, Expected: n.Version}}}, t0); err != nil {
		t.Fatal(err)
	}
	// A different node takes the old name; the report still lands on the original.
	if _, err := s.CreateNode(ctx, store.Node{ID: "t9", Name: "old-name", Address: "h2", Port: 22, Transport: "ssh",
		HostKeyPolicy: "tofu", Source: "manual", Enabled: true}, t0); err != nil {
		t.Fatal(err)
	}
	if err := source(s, inventory.AllNodes{}, nil).ObserveHostKey(ctx, runID, attempt, "a1", "old-name", "ssh-ed25519 KEY"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetNodeByID(ctx, "t1"); got.HostKeyObserved != "ssh-ed25519 KEY" {
		t.Fatalf("original node observed %q", got.HostKeyObserved)
	}
	if got, _ := s.GetNodeByID(ctx, "t9"); got.HostKeyObserved != "" {
		t.Fatal("the node that took the name recorded the key")
	}
}

// countingNodes counts the node reads that go to the store one at a time and
// the batch reads, to pin the O(1) queries per chunk of a big inventory step.
type countingNodes struct {
	store.Store
	single, batch int
}

func (c *countingNodes) GetNode(ctx context.Context, name string) (store.Node, error) {
	c.single++
	return c.Store.GetNode(ctx, name)
}
func (c *countingNodes) GetNodeByID(ctx context.Context, id string) (store.Node, error) {
	c.single++
	return c.Store.GetNodeByID(ctx, id)
}
func (c *countingNodes) RunTargetNodeID(ctx context.Context, runID, name string) (string, error) {
	c.single++
	return c.Store.RunTargetNodeID(ctx, runID, name)
}
func (c *countingNodes) GetNodesByIDs(ctx context.Context, ids []string) (map[string]store.Node, error) {
	c.batch++
	return c.Store.GetNodesByIDs(ctx, ids)
}
func (c *countingNodes) GetNodesByNames(ctx context.Context, names []string) (map[string]store.Node, error) {
	c.batch++
	return c.Store.GetNodesByNames(ctx, names)
}
func (c *countingNodes) RunTargetNodeIDs(ctx context.Context, runID string, names []string) (map[string]string, error) {
	c.batch++
	return c.Store.RunTargetNodeIDs(ctx, runID, names)
}

// The batch answers are the answers Node and NodePool give name by name: frozen
// ids, names outside the snapshot, unknown, disabled, out of scope, insecure.
func TestBatchMatchesSingleLookups(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	web := put(t, s, "web-1", true)
	put(t, s, "extra", true)
	put(t, s, "off", false)
	put(t, s, "denied", true)
	ins := put(t, s, "insecure", true)
	ins.HostKeyPolicy = inventory.PolicyInsecure
	if err := s.ApplyNodeDiff(ctx, store.NodeDiff{Update: []store.NodeUpdate{{Node: ins, Expected: 1}}}, t0); err != nil {
		t.Fatal(err)
	}
	job, ver, err := s.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "j", Spec: []byte(`{}`), Checksum: "c"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := s.CreateRun(ctx, store.NewRun{JobID: job.ID, JobVersionID: ver.ID,
		Targets: []store.RunTarget{{Name: "web-1", NodeID: web.ID, Node: []byte(`{"name":"web-1"}`)}}}, t0)
	if err != nil {
		t.Fatal(err)
	}
	renamed := web
	renamed.Name = "web-renamed"
	if err := s.ApplyNodeDiff(ctx, store.NodeDiff{Update: []store.NodeUpdate{{Node: renamed, Expected: web.Version}}}, t0); err != nil {
		t.Fatal(err)
	}

	names := []string{"web-1", "extra", "off", "denied", "insecure", "nope"}
	src := source(s, nameScope{deny: "denied"}, nil)
	nodes, err := src.Nodes(ctx, tree.ID, names)
	if err != nil {
		t.Fatal(err)
	}
	pools, err := src.NodePools(ctx, tree.ID, names)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		info, serr := src.Node(ctx, tree.ID, name)
		if !errors.Is(nodes[i].Err, serr) && (nodes[i].Err != nil || serr != nil) {
			t.Errorf("%s: batch error %v, single %v", name, nodes[i].Err, serr)
		}
		if serr == nil && (nodes[i].Info.Name != info.Name || nodes[i].Info.Address != info.Address || nodes[i].Info.HostKey != info.HostKey) {
			t.Errorf("%s: batch info %+v, single %+v", name, nodes[i].Info, info)
		}
		pool, perr := src.NodePool(ctx, tree.ID, name)
		if !errors.Is(pools[i].Err, perr) && (pools[i].Err != nil || perr != nil) || pools[i].Pool != pool {
			t.Errorf("%s: batch pool %q/%v, single %q/%v", name, pools[i].Pool, pools[i].Err, pool, perr)
		}
	}
}

func TestBatchQueriesDoNotGrowWithTheNodeCount(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	const total = 1000
	names := make([]string, total)
	for i := range names {
		names[i] = fmt.Sprintf("host-%04d", i)
		put(t, st, names[i], true)
	}
	cs := &countingNodes{Store: st}
	src := source(cs, inventory.AllNodes{}, nil)
	res, err := src.Nodes(ctx, "run", names)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range res {
		if r.Err != nil || r.Info.Name != names[i] {
			t.Fatalf("result %d = %+v", i, r)
		}
	}
	if cs.single != 0 {
		t.Errorf("%d one-by-one reads for %d names", cs.single, total)
	}
	// 1000 names: 1 frozen-target read (2 chunks), 0 id reads (nothing frozen), 1 name read (2 chunks).
	if cs.batch > 3 {
		t.Errorf("%d batch calls for %d names; want a constant number", cs.batch, total)
	}
}
