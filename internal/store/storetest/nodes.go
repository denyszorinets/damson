package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func node(id, name string) store.Node {
	return store.Node{
		ID: id, Name: name, Address: name + ".example.com", Port: 22, Transport: "ssh", OS: "linux", Pool: "default",
		Tags: []string{"db", "prod"}, Attributes: map[string]any{"env": "prod", "n": float64(3), "ok": true},
		Credential: "secret://_inventory/key", HostKeyPolicy: "strict", Source: "manual", Enabled: true,
	}
}

func testNodes(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)

	n, err := s.CreateNode(ctx, node("n1", "web-1"), t0)
	if err != nil || n.Version != 1 || !n.Enabled || n.HostKeyPolicy != "strict" || n.Port != 22 ||
		len(n.Tags) != 2 || n.Attributes["env"] != "prod" || n.Attributes["n"] != float64(3) || n.Attributes["ok"] != true ||
		!n.CreatedAt.Equal(t0) || n.HostKeyObservedAt != nil {
		t.Fatalf("CreateNode = %+v, %v", n, err)
	}
	if n.CreatedAt.Location() != time.UTC || n.UpdatedAt.Location() != time.UTC {
		t.Fatalf("times are not UTC: %v %v", n.CreatedAt.Location(), n.UpdatedAt.Location())
	}
	if got, err := s.GetNode(ctx, "web-1"); err != nil || got.ID != "n1" {
		t.Fatalf("GetNode = %+v, %v", got, err)
	}
	if _, err := s.GetNode(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	// Uniqueness: name, name differing only in case, (source, external_id).
	if _, err := s.CreateNode(ctx, node("n2", "web-1"), t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := s.CreateNode(ctx, node("n3", "WEB-1"), t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("case-only duplicate: %v", err)
	}
	a, b := node("e1", "ext-a"), node("e2", "ext-b")
	a.Source, a.ExternalID, b.Source, b.ExternalID = "sync1", "x", "sync1", "x"
	if _, err := s.CreateNode(ctx, a, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNode(ctx, b, t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate (source, external_id): %v", err)
	}
	// Empty external ids never collide; another source may reuse an id.
	for i, src := range []string{"manual", "manual", "sync2"} {
		c := node(fmt.Sprintf("m%d", i), fmt.Sprintf("m-%d", i))
		c.Source = src
		if src == "sync2" {
			c.ExternalID = "x"
		}
		if _, err := s.CreateNode(ctx, c, t0); err != nil {
			t.Fatalf("node %d: %v", i, err)
		}
	}

	// Update: version bump, immutable fields, observed key untouched.
	t1 := t0.Add(time.Minute)
	u := n
	u.Address, u.Enabled, u.Tags, u.Source, u.Name = "10.0.0.9", false, []string{"x"}, "hijack", "renamed"
	got, err := s.UpdateNode(ctx, u, 1, t1)
	if err != nil || got.Version != 2 || got.Address != "10.0.0.9" || got.Enabled || got.Source != "manual" || got.Name != "web-1" ||
		!got.UpdatedAt.Equal(t1) || !got.CreatedAt.Equal(t0) || len(got.Tags) != 1 {
		t.Fatalf("UpdateNode = %+v, %v", got, err)
	}
	if _, err := s.UpdateNode(ctx, u, 1, t1); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale update: %v", err)
	}
	u.ID = "gone"
	if _, err := s.UpdateNode(ctx, u, 1, t1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update of a missing id: %v", err)
	}

	// Delete is a CAS by id; a recreated name is not the old node.
	if err := s.DeleteNode(ctx, "n1", 1); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := s.DeleteNode(ctx, "n1", 2); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteNode(ctx, "n1", 2); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete again: %v", err)
	}
	if _, err := s.CreateNode(ctx, node("n1b", "web-1"), t0); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if _, err := s.UpdateNode(ctx, node("n1", "web-1"), 1, t1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update through the old id: %v", err)
	}
}

func testNodeList(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	// Mixed case, dots, dashes and digits sort bytewise on both databases.
	names := []string{"b", "B1", "a.1", "a-1", "a1", "Zed", "10", "9"}
	for i, name := range names {
		n := node(fmt.Sprintf("id%d", i), name)
		n.Enabled = i%2 == 0
		if i < 3 {
			n.Source = "sync1"
			n.ExternalID = name
		}
		if _, err := s.CreateNode(ctx, n, t0); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	want := []string{"10", "9", "B1", "Zed", "a-1", "a.1", "a1", "b"}
	all, err := s.ListNodes(ctx, store.NodeFilter{Limit: 100})
	if err != nil || len(all) != len(want) {
		t.Fatalf("ListNodes = %d nodes, %v", len(all), err)
	}
	for i, n := range all {
		if n.Name != want[i] {
			t.Fatalf("order[%d] = %q, want %q (all: %v)", i, n.Name, want[i], func() (o []string) {
				for _, x := range all {
					o = append(o, x.Name)
				}
				return
			}())
		}
	}
	// Keyset paging walks the same order.
	var walked []string
	after := ""
	for {
		page, err := s.ListNodes(ctx, store.NodeFilter{AfterName: after, Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range page {
			walked = append(walked, n.Name)
		}
		if len(page) < 3 {
			break
		}
		after = page[len(page)-1].Name
	}
	if fmt.Sprint(walked) != fmt.Sprint(want) {
		t.Fatalf("paged = %v, want %v", walked, want)
	}
	sync1, _ := s.ListNodes(ctx, store.NodeFilter{Source: "sync1", Limit: 100})
	if len(sync1) != 3 {
		t.Fatalf("source filter = %d, want 3", len(sync1))
	}
	// A zero or negative limit means the default on both databases.
	for _, l := range []int{0, -1} {
		if page, err := s.ListNodes(ctx, store.NodeFilter{Limit: l}); err != nil || len(page) != len(want) {
			t.Fatalf("Limit %d = %d nodes, %v", l, len(page), err)
		}
	}
	yes, no := true, false
	on, _ := s.ListNodes(ctx, store.NodeFilter{Enabled: &yes, Limit: 100})
	off, _ := s.ListNodes(ctx, store.NodeFilter{Enabled: &no, Limit: 100})
	if len(on) != 4 || len(off) != 4 {
		t.Fatalf("enabled filter = %d on, %d off; want 4 and 4", len(on), len(off))
	}
}

func testNodeRaces(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	if _, err := s.CreateNode(ctx, node("r1", "race"), t0); err != nil {
		t.Fatal(err)
	}
	var wins, stale atomic.Int32
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := node("r1", "race")
			u.Address = fmt.Sprintf("10.0.0.%d", i)
			_, err := s.UpdateNode(ctx, u, 1, t0)
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, store.ErrStale):
				stale.Add(1)
			default:
				t.Errorf("update: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || stale.Load() != 5 {
		t.Fatalf("update race: %d wins, %d stale", wins.Load(), stale.Load())
	}
}
