package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
