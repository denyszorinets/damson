package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
)

func targetsOf(n int) []store.RunTarget {
	out := make([]store.RunTarget, n)
	for i := range out {
		name := fmt.Sprintf("n%04d", i)
		out[i] = store.RunTarget{Name: name, NodeID: "id-" + name, Node: json.RawMessage(fmt.Sprintf(`{"name":%q,"os":"linux"}`, name))}
	}
	return out
}

func testRunTargets(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	job, ver := mustJob(t, s, "targets")
	create := func(ts []store.RunTarget) (store.RunTree, error) {
		return s.CreateRun(ctx, store.NewRun{
			JobID: job.ID, JobVersionID: ver.ID, Targets: ts,
			Steps: []store.NewStepInstance{{Path: "a", Plugin: "shell", Execution: execSpec("p")}},
		}, t0)
	}

	// The snapshot is written with the run and read back in order.
	tree, err := create(targetsOf(5))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ListRunTargets(ctx, tree.ID)
	if err != nil || len(got) != 5 {
		t.Fatalf("ListRunTargets = %v, %v", got, err)
	}
	for i, g := range got {
		if g.Position != i+1 || g.Name != fmt.Sprintf("n%04d", i) || g.NodeID != "id-"+g.Name {
			t.Fatalf("target %d = %+v", i, g)
		}
		var m map[string]any
		if err := json.Unmarshal(g.Node, &m); err != nil || m["name"] != g.Name || m["os"] != "linux" {
			t.Fatalf("node %d = %s (%v)", i, g.Node, err)
		}
	}
	// Pages by position.
	page, err := s.ListRunTargetsPage(ctx, tree.ID, 0, 2)
	if err != nil || len(page) != 2 || page[1].Position != 2 {
		t.Fatalf("page 1 = %v, %v", page, err)
	}
	page, _ = s.ListRunTargetsPage(ctx, tree.ID, 4, 10)
	if len(page) != 1 || page[0].Name != "n0004" {
		t.Fatalf("last page = %v", page)
	}

	// A frozen name resolves to its node id; case matters; unknown names fail.
	if id, err := s.RunTargetNodeID(ctx, tree.ID, "n0002"); err != nil || id != "id-n0002" {
		t.Fatalf("RunTargetNodeID = %q, %v", id, err)
	}
	for _, name := range []string{"N0002", "nope", ""} {
		if _, err := s.RunTargetNodeID(ctx, tree.ID, name); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("RunTargetNodeID(%q): %v; want ErrNotFound", name, err)
		}
	}
	// Another run does not see them.
	other, err := create(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListRunTargets(ctx, other.ID); len(got) != 0 {
		t.Fatalf("a run without a snapshot lists %v", got)
	}
	if _, err := s.RunTargetNodeID(ctx, other.ID, "n0002"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("name of another run's snapshot: %v", err)
	}

	// The limit is exact; anything malformed is refused and leaves no run.
	if _, err := create(targetsOf(store.MaxRunTargets)); err != nil {
		t.Fatalf("exactly %d targets: %v", store.MaxRunTargets, err)
	}
	before, _ := s.ListRuns(ctx, job.ID, "", 100)
	dup := targetsOf(3)
	dup[2].Name = dup[0].Name
	dupID := targetsOf(3)
	dupID[2].NodeID = dupID[0].NodeID
	for name, ts := range map[string][]store.RunTarget{
		"too many":       targetsOf(store.MaxRunTargets + 1),
		"duplicate name": dup,
		"duplicate id":   dupID,
		"empty name":     {{Name: "", NodeID: "x", Node: json.RawMessage(`{}`)}},
		"empty id":       {{Name: "x", NodeID: "", Node: json.RawMessage(`{}`)}},
		"not an object":  {{Name: "x", NodeID: "x", Node: json.RawMessage(`[1]`)}},
		"bad json":       {{Name: "x", NodeID: "x", Node: json.RawMessage(`{`)}},
		"missing node":   {{Name: "x", NodeID: "x"}},
	} {
		if _, err := create(ts); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: %v; want ErrInvalid", name, err)
		}
	}
	after, _ := s.ListRuns(ctx, job.ID, "", 100)
	if len(after.Runs) != len(before.Runs) {
		t.Fatalf("a refused snapshot left %d run(s) behind", len(after.Runs)-len(before.Runs))
	}

	// Page sizes are clamped the same way on both stores.
	for _, lim := range []int{0, -1, store.MaxRunTargets + 1} {
		if _, err := s.ListRunTargetsPage(ctx, tree.ID, 0, lim); err != nil {
			t.Fatalf("page limit %d: %v", lim, err)
		}
	}
	if _, err := create([]store.RunTarget{{Name: "x", NodeID: "x", Node: json.RawMessage(`{"a":"` + strings.Repeat("x", store.MaxRunTargetNodeBytes) + `"}`)}}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("oversized node: %v", err)
	}
	if _, err := create([]store.RunTarget{{Name: "a\x00b", NodeID: "x", Node: json.RawMessage(`{}`)}}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("NUL in name: %v", err)
	}

	// Targets roll back with the run: a failing step insert, and a failing tx.
	dupStep := store.NewRun{ID: "0192a000-0000-7000-8000-0000000000d1", JobID: job.ID, JobVersionID: ver.ID, Targets: targetsOf(2),
		Steps: []store.NewStepInstance{
			{Path: "a", Plugin: "shell", Execution: execSpec("p")},
			{Path: "a", Plugin: "shell", Execution: execSpec("p")},
		}}
	if _, err := s.CreateRun(ctx, dupStep, t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate step path: %v", err)
	}
	if got, _ := s.ListRunTargets(ctx, "0192a000-0000-7000-8000-0000000000d1"); len(got) != 0 {
		t.Fatalf("targets of a failed CreateRun survived: %d", len(got))
	}
	errBoom := errors.New("boom")
	err = s.WithTx(ctx, func(tx store.Tx) error {
		if _, err := tx.CreateRun(ctx, store.NewRun{ID: "0192a000-0000-7000-8000-0000000000d2", JobID: job.ID, JobVersionID: ver.ID, Targets: targetsOf(2),
			Steps: []store.NewStepInstance{{Path: "a", Plugin: "shell", Execution: execSpec("p")}}}, t0); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("WithTx = %v", err)
	}
	if got, _ := s.ListRunTargets(ctx, "0192a000-0000-7000-8000-0000000000d2"); len(got) != 0 {
		t.Fatalf("targets of a rolled back tx survived: %d", len(got))
	}

	// A node by id.
	if _, err := s.CreateNode(ctx, node("nid-1", "by-id"), t0); err != nil {
		t.Fatal(err)
	}
	if n, err := s.GetNodeByID(ctx, "nid-1"); err != nil || n.Name != "by-id" {
		t.Fatalf("GetNodeByID = %+v, %v", n, err)
	}
	if _, err := s.GetNodeByID(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetNodeByID(missing): %v", err)
	}
}

func testObservedHostKey(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	ex := claimedOne(t, s) // owned by agent-1, dispatched
	mk := func(id, name, policy, pinned string) {
		n := node(id, name)
		n.HostKeyPolicy, n.HostKey = policy, pinned
		if _, err := s.CreateNode(ctx, n, t0); err != nil {
			t.Fatal(err)
		}
	}
	mk("h1", "tofu-new", "tofu", "")
	mk("h2", "tofu-pinned", "tofu", "ssh-ed25519 AAAA")
	mk("h3", "strict-1", "strict", "ssh-ed25519 AAAA")
	observe := func(id, key, attempt, agent string) bool {
		t.Helper()
		ok, err := s.ObserveNodeHostKey(ctx, id, key, attempt, agent, t0)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// Fenced: another agent's or an unknown attempt writes nothing.
	if observe("h1", "ssh-ed25519 X", ex.AttemptID, "other-agent") || observe("h1", "ssh-ed25519 X", "unknown", "agent-1") {
		t.Fatal("a foreign attempt recorded a key")
	}
	if !observe("h1", "ssh-ed25519 FIRST", ex.AttemptID, "agent-1") {
		t.Fatal("the first observation was not recorded")
	}
	n, _ := s.GetNodeByID(ctx, "h1")
	if n.HostKeyObserved != "ssh-ed25519 FIRST" || n.HostKeyObservedAt == nil || !n.HostKeyObservedAt.Equal(t0) || n.Version != 1 {
		t.Fatalf("node after observation = %+v", n)
	}
	// Never overwritten: a second key, a replay, a pinned node, a strict node, a missing node.
	for id, key := range map[string]string{"h1": "ssh-ed25519 OTHER", "h2": "ssh-ed25519 X", "h3": "ssh-ed25519 X", "nope": "ssh-ed25519 X"} {
		if observe(id, key, ex.AttemptID, "agent-1") {
			t.Errorf("observe %s wrote", id)
		}
	}
	if observe("h1", "ssh-ed25519 FIRST", ex.AttemptID, "agent-1") {
		t.Error("a replay wrote again")
	}
	if n, _ := s.GetNodeByID(ctx, "h1"); n.HostKeyObserved != "ssh-ed25519 FIRST" {
		t.Fatalf("observed key changed to %q", n.HostKeyObserved)
	}
	// Forgetting lets the next connection observe again; a finished attempt cannot.
	if err := s.ClearNodeHostKeyObserved(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.GetNodeByID(ctx, "h1"); n.HostKeyObserved != "" || n.HostKeyObservedAt != nil {
		t.Fatalf("not cleared: %+v", n)
	}
	if err := s.MarkExecutionRunning(ctx, ex.ID, ex.AttemptID, claimNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteExecution(ctx, store.Completion{ExecutionID: ex.ID, AttemptID: ex.AttemptID, State: store.ExecSucceeded}, claimNow); err != nil {
		t.Fatal(err)
	}
	if observe("h1", "ssh-ed25519 LATE", ex.AttemptID, "agent-1") {
		t.Fatal("a finished attempt recorded a key")
	}
	if err := s.ClearNodeHostKeyObserved(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("clear missing = %v", err)
	}
}

// Concurrent first observations: exactly one key wins.
func testObservedHostKeyRace(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	ex := claimedOne(t, s)
	n := node("r1", "race")
	n.HostKeyPolicy = "tofu"
	if _, err := s.CreateNode(ctx, n, t0); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := s.ObserveNodeHostKey(ctx, "r1", fmt.Sprintf("ssh-ed25519 K%d", i), ex.AttemptID, "agent-1", t0); err == nil && ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d observations won; want exactly one", wins.Load())
	}
}
