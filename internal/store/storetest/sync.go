package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/store"
)

func desired(name, ext string) store.Node {
	return store.Node{
		Name: name, ExternalID: ext, Address: name + ".example.com", Port: 22, Transport: "ssh", HostKeyPolicy: "strict",
		Tags: []string{"b", "a"}, Attributes: map[string]any{"n": 1}, Enabled: true,
	}
}

func versionOf(t *testing.T, s store.Store, name string) int64 {
	t.Helper()
	n, err := s.GetNode(context.Background(), name)
	if err != nil {
		t.Fatalf("GetNode(%s): %v", name, err)
	}
	return n.Version
}

func testNodeSync(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	sync := func(prune, dry bool, ds ...store.Node) (inventory.Diff, error) {
		return inventory.Sync(ctx, s, "sync1", ds, prune, dry, t0)
	}

	d, err := sync(true, false, desired("a", "1"), desired("b", "2"))
	if err != nil || len(d.Created) != 2 {
		t.Fatalf("first sync = %+v, %v", d, err)
	}
	a, _ := s.GetNode(ctx, "a")
	if a.Source != "sync1" || a.ExternalID != "1" || a.Version != 1 || len(a.Tags) != 2 || a.Tags[0] != "a" {
		t.Fatalf("created node = %+v", a)
	}

	// Replay: nothing is written and no version moves.
	d, err = sync(true, false, desired("a", "1"), desired("b", "2"))
	if err != nil || len(d.Unchanged) != 2 || len(d.Created)+len(d.Updated)+len(d.Deleted) != 0 {
		t.Fatalf("replay = %+v, %v", d, err)
	}
	if versionOf(t, s, "a") != 1 || versionOf(t, s, "b") != 1 {
		t.Fatal("a replay bumped a version")
	}

	// A change touches only that node; dry run writes nothing.
	changed := desired("b", "2")
	changed.Address = "10.0.0.2"
	if d, err = sync(true, true, desired("a", "1"), changed); err != nil || len(d.Updated) != 1 {
		t.Fatalf("dry run = %+v, %v", d, err)
	}
	if n, _ := s.GetNode(ctx, "b"); n.Address == "10.0.0.2" || n.Version != 1 {
		t.Fatalf("dry run wrote: %+v", n)
	}
	if d, err = sync(true, false, desired("a", "1"), changed); err != nil || len(d.Updated) != 1 {
		t.Fatalf("update = %+v, %v", d, err)
	}
	if versionOf(t, s, "a") != 1 || versionOf(t, s, "b") != 2 {
		t.Fatal("only b should have moved")
	}

	// Rename by external id keeps the node id.
	before, _ := s.GetNode(ctx, "b")
	renamed := desired("b2", "2")
	renamed.Address = "10.0.0.2"
	if d, err = sync(true, false, desired("a", "1"), renamed); err != nil || len(d.Updated) != 1 {
		t.Fatalf("rename = %+v, %v", d, err)
	}
	if after, err := s.GetNode(ctx, "b2"); err != nil || after.ID != before.ID {
		t.Fatalf("rename lost the node: %+v, %v", after, err)
	}

	// Nodes of other sources are never pruned or taken over.
	if _, err := s.CreateNode(ctx, node("m1", "manual-1"), t0); err != nil {
		t.Fatal(err)
	}
	other := node("o1", "other-1")
	other.Source, other.ExternalID = "sync2", "x"
	if _, err := s.CreateNode(ctx, other, t0); err != nil {
		t.Fatal(err)
	}
	if d, err = sync(true, false, desired("a", "1")); err != nil || len(d.Deleted) != 1 || d.Deleted[0] != "b2" {
		t.Fatalf("prune = %+v, %v", d, err)
	}
	for _, name := range []string{"manual-1", "other-1"} {
		if _, err := s.GetNode(ctx, name); err != nil {
			t.Fatalf("prune touched %s: %v", name, err)
		}
	}
	// Without prune the rest stays; a clash with another source fails the whole call.
	if d, err = sync(false, false, desired("a", "1"), desired("c", "3")); err != nil || len(d.Created) != 1 {
		t.Fatalf("no-prune sync = %+v, %v", d, err)
	}
	d, err = sync(true, false, desired("a", "1"), desired("manual-1", "9"), desired("zz", "10"))
	if !errors.Is(err, inventory.ErrSyncConflict) || len(d.Conflicts) != 1 || d.Conflicts[0].OwnedBy != "manual" {
		t.Fatalf("conflict = %+v, %v", d, err)
	}
	if _, err := s.GetNode(ctx, "zz"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a conflicting sync wrote part of its changes")
	}
	// Case-only clashes with another source are conflicts, not late errors.
	d, err = sync(true, true, desired("a", "1"), desired("MANUAL-1", "9"))
	if !errors.Is(err, inventory.ErrSyncConflict) || len(d.Conflicts) != 1 || d.Conflicts[0].OwnedBy != "manual" {
		t.Fatalf("case-only clash = %+v, %v", d, err)
	}
	// A swap of two own names is reported, also in a dry run.
	if _, err = sync(false, false, desired("a", "1"), desired("c", "3")); err != nil {
		t.Fatal(err)
	}
	d, err = sync(false, true, desired("c", "1"), desired("a", "3"))
	if !errors.Is(err, inventory.ErrSyncConflict) || len(d.Conflicts) != 2 {
		t.Fatalf("swap = %+v, %v", d, err)
	}
	// A removed node's name can be reused by a new node in the same call.
	if d, err = sync(true, false, desired("a", "1"), desired("c", "33")); err != nil || len(d.Created) != 1 || len(d.Deleted) != 1 {
		t.Fatalf("name reuse = %+v, %v", d, err)
	}
	// An invalid desired node is refused before any write.
	bad := desired("bad name!", "5")
	if _, err = sync(true, false, desired("a", "1"), bad); !errors.Is(err, inventory.ErrBadDesired) {
		t.Fatalf("invalid desired: %v", err)
	}
}

// ApplyNodeDiff is all or nothing.
func testNodeDiffAtomic(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	a, err := s.CreateNode(ctx, node("a1", "a"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNode(ctx, node("b1", "b"), t0); err != nil {
		t.Fatal(err)
	}
	upd := a
	upd.Address = "changed"
	// The update is fine but the create clashes: both roll back.
	err = s.ApplyNodeDiff(ctx, store.NodeDiff{
		Update: []store.NodeUpdate{{Node: upd, Expected: 1}},
		Create: []store.Node{node("c1", "b")},
	}, t0)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("clashing create: %v", err)
	}
	if got, _ := s.GetNode(ctx, "a"); got.Address == "changed" || got.Version != 1 {
		t.Fatalf("update survived a failed diff: %+v", got)
	}
	// A stale update rolls back the delete before it.
	err = s.ApplyNodeDiff(ctx, store.NodeDiff{
		Delete: []store.NodeDelete{{ID: "b1", Expected: 1}},
		Update: []store.NodeUpdate{{Node: upd, Expected: 7}},
	}, t0)
	if !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale update: %v", err)
	}
	if _, err := s.GetNode(ctx, "b"); err != nil {
		t.Fatalf("delete survived a failed diff: %v", err)
	}
	// Update-path clashes roll back too: a rename into an existing name, and an
	// external id another node of the same source already has.
	c, err := s.CreateNode(ctx, func() store.Node { n := node("c1", "c"); n.Source, n.ExternalID = "sync1", "ext-c"; return n }(), t0)
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.CreateNode(ctx, func() store.Node { n := node("e1", "e"); n.Source, n.ExternalID = "sync1", "ext-e"; return n }(), t0)
	if err != nil {
		t.Fatal(err)
	}
	rename := c
	rename.Name = "A"
	err = s.ApplyNodeDiff(ctx, store.NodeDiff{
		Update: []store.NodeUpdate{{Node: upd, Expected: 1}, {Node: rename, Expected: 1}},
	}, t0)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("rename into an existing name: %v", err)
	}
	dup := e
	dup.ExternalID = "ext-c"
	err = s.ApplyNodeDiff(ctx, store.NodeDiff{
		Update: []store.NodeUpdate{{Node: upd, Expected: 1}, {Node: dup, Expected: 1}},
	}, t0)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate external id: %v", err)
	}
	if got, _ := s.GetNode(ctx, "a"); got.Address == "changed" || got.Version != 1 {
		t.Fatalf("an update survived a failed diff: %+v", got)
	}
	// A delete frees its name for a create in the same diff.
	if err := s.ApplyNodeDiff(ctx, store.NodeDiff{
		Delete: []store.NodeDelete{{ID: "b1", Expected: 1}},
		Create: []store.Node{node("b2", "b")},
	}, t0); err != nil {
		t.Fatalf("delete then create: %v", err)
	}
}

func testNodeSyncRace(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := inventory.Sync(ctx, s, "sync1", []store.Node{desired("a", "1"), desired("b", "2")}, true, false, time.Now())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrStale):
		default:
			t.Errorf("sync: %v", err)
		}
	}
	if ok != 4 {
		t.Fatalf("%d of 4 concurrent syncs succeeded; the losers must re-plan to an empty diff", ok)
	}
	all, _ := s.ListNodes(ctx, store.NodeFilter{Limit: 100})
	if len(all) != 2 {
		t.Fatalf("%d nodes after concurrent syncs, want 2", len(all))
	}
}

func testNodeBatch(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	batch := func(dry bool, ups []store.Node, dels ...inventory.DeleteRef) (inventory.Diff, error) {
		return inventory.Batch(ctx, s, "sync1", ups, dels, dry, t0)
	}
	if _, err := inventory.Sync(ctx, s, "sync1", []store.Node{desired("a", "1"), desired("b", "2"), desired("c", "3")}, true, false, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNode(ctx, node("m1", "manual-1"), t0); err != nil {
		t.Fatal(err)
	}

	// Upserts never prune: b and c stay when only a is sent.
	changed := desired("a", "1")
	changed.Address = "10.0.0.1"
	d, err := batch(false, []store.Node{changed})
	if err != nil || len(d.Updated) != 1 || len(d.Deleted) != 0 {
		t.Fatalf("upsert = %+v, %v", d, err)
	}
	if _, err := s.GetNode(ctx, "b"); err != nil {
		t.Fatalf("a batch pruned: %v", err)
	}

	// Deletes by external id and by name; dry run writes nothing.
	if d, err = batch(true, nil, inventory.DeleteRef{ExternalID: "2"}, inventory.DeleteRef{Name: "c"}); err != nil || len(d.Deleted) != 2 {
		t.Fatalf("dry run = %+v, %v", d, err)
	}
	if _, err := s.GetNode(ctx, "b"); err != nil {
		t.Fatal("a dry run deleted")
	}
	if d, err = batch(false, nil, inventory.DeleteRef{ExternalID: "2"}, inventory.DeleteRef{Name: "c"}); err != nil || len(d.Deleted) != 2 {
		t.Fatalf("delete = %+v, %v", d, err)
	}
	for _, name := range []string{"b", "c"} {
		if _, err := s.GetNode(ctx, name); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s still there: %v", name, err)
		}
	}
	// Repeating a delete is fine, and a delete never reaches another source's node.
	if d, err = batch(false, nil, inventory.DeleteRef{ExternalID: "2"}, inventory.DeleteRef{Name: "manual-1"}); err != nil || len(d.Deleted) != 0 {
		t.Fatalf("repeat = %+v, %v", d, err)
	}
	if _, err := s.GetNode(ctx, "manual-1"); err != nil {
		t.Fatalf("a batch deleted another source's node: %v", err)
	}
	// Re-identifying a host: the delete frees the name for a node with a new external id.
	if _, err := inventory.Sync(ctx, s, "sync1", []store.Node{desired("a", "1"), desired("b", "2")}, true, false, t0); err != nil {
		t.Fatal(err)
	}
	if d, err = batch(false, []store.Node{desired("b", "9")}, inventory.DeleteRef{ExternalID: "2"}); err != nil || len(d.Deleted) != 1 || len(d.Created) != 1 {
		t.Fatalf("re-identification = %+v, %v", d, err)
	}
	if n, _ := s.GetNode(ctx, "b"); n.ExternalID != "9" {
		t.Fatalf("b = %+v", n)
	}
	// Upsert and delete of the same node in one call, and an empty delete, are refused.
	if _, err = batch(false, []store.Node{desired("a", "1")}, inventory.DeleteRef{ExternalID: "1"}); !errors.Is(err, inventory.ErrBadDesired) {
		t.Fatalf("upsert + delete: %v", err)
	}
	if _, err = batch(false, nil, inventory.DeleteRef{}); !errors.Is(err, inventory.ErrBadDesired) {
		t.Fatalf("empty delete: %v", err)
	}
}
