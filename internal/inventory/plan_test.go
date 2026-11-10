package inventory

import (
	"reflect"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
)

func nd(name, ext string) store.Node {
	return store.Node{
		Name: name, ExternalID: ext, Address: name + ".example.com", Port: 22, Transport: TransportSSH,
		HostKeyPolicy: PolicyStrict, Source: "s1", Enabled: true, Tags: []string{}, Attributes: map[string]any{},
	}
}

func stored(id string, ver int64, n store.Node) store.Node {
	n.ID, n.Version = id, ver
	return n
}

func noOthers(string) (string, bool) { return "", false }

func TestPlanSync(t *testing.T) {
	changed := nd("b", "2")
	changed.Address = "elsewhere"
	renamed := nd("c-new", "3")
	cases := []struct {
		name                                          string
		desired, own                                  []store.Node
		others                                        func(string) (string, bool)
		prune                                         bool
		created, updated, deleted, unchanged, clashes []string
	}{
		{name: "everything new", desired: []store.Node{nd("a", "1"), nd("b", "2")}, created: []string{"a", "b"}},
		{name: "replay is unchanged", desired: []store.Node{nd("a", "1")}, own: []store.Node{stored("i1", 3, nd("a", "1"))}, unchanged: []string{"a"}},
		{name: "changed field updates", desired: []store.Node{changed}, own: []store.Node{stored("i2", 1, nd("b", "2"))}, updated: []string{"b"}},
		{name: "rename by external id", desired: []store.Node{renamed}, own: []store.Node{stored("i3", 1, nd("c", "3"))}, updated: []string{"c-new"}},
		{name: "adopt by name", desired: []store.Node{nd("a", "1")}, own: []store.Node{stored("i1", 1, nd("a", ""))}, updated: []string{"a"}},
		{name: "case-only name match", desired: []store.Node{nd("A", "1")}, own: []store.Node{stored("i1", 1, nd("a", "1"))}, updated: []string{"A"}},
		{name: "prune deletes the rest", desired: []store.Node{nd("a", "1")}, own: []store.Node{stored("i1", 1, nd("a", "1")), stored("i9", 2, nd("gone", "9"))}, prune: true, unchanged: []string{"a"}, deleted: []string{"gone"}},
		{name: "no prune keeps the rest", desired: []store.Node{nd("a", "1")}, own: []store.Node{stored("i1", 1, nd("a", "1")), stored("i9", 2, nd("gone", "9"))}, unchanged: []string{"a"}},
		{name: "pruned name is reusable", desired: []store.Node{nd("a", "2")}, own: []store.Node{stored("i1", 1, nd("a", "1"))}, prune: true, created: []string{"a"}, deleted: []string{"a"}},
		{name: "name held by an own node that stays is a clash", desired: []store.Node{nd("a", "2")}, own: []store.Node{stored("i1", 1, nd("a", "1"))}, clashes: []string{"a"}},
		{name: "rename into another source's name", desired: []store.Node{nd("manual-1", "1")}, own: []store.Node{stored("i1", 1, nd("a", "1"))},
			others: func(n string) (string, bool) { return "manual", n == "manual-1" }, clashes: []string{"manual-1"}},
		{name: "case-only name held by another source", desired: []store.Node{nd("MANUAL-1", "1")},
			others: func(n string) (string, bool) { return "manual", strings.EqualFold(n, "manual-1") }, clashes: []string{"MANUAL-1"}},
		{name: "swap is a conflict", desired: []store.Node{nd("b", "1"), nd("a", "2")}, own: []store.Node{stored("i1", 1, nd("a", "1")), stored("i2", 1, nd("b", "2"))}, clashes: []string{"b", "a"}},
		{name: "chain in the wrong order is a conflict", desired: []store.Node{nd("b", "1"), nd("c", "2")}, own: []store.Node{stored("i1", 1, nd("a", "1")), stored("i2", 1, nd("b", "2"))}, clashes: []string{"b"}, updated: []string{"c"}},
		{name: "case-only self rename", desired: []store.Node{nd("A", "1")}, own: []store.Node{stored("i1", 1, nd("a", "1"))}, updated: []string{"A"}},
		{name: "rename into a pruned node's name", desired: []store.Node{nd("b", "1")}, own: []store.Node{stored("i1", 1, nd("a", "1")), stored("i2", 1, nd("b", "2"))}, prune: true, updated: []string{"b"}, deleted: []string{"b"}},
		{name: "other source holds the name", desired: []store.Node{nd("a", "1")}, others: func(n string) (string, bool) { return "manual", n == "a" }, clashes: []string{"a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			others := c.others
			if others == nil {
				others = noOthers
			}
			plan, diff := PlanSync("s1", c.desired, c.own, others, c.prune)
			got := map[string][]string{"created": diff.Created, "updated": diff.Updated, "deleted": diff.Deleted, "unchanged": diff.Unchanged}
			want := map[string][]string{"created": c.created, "updated": c.updated, "deleted": c.deleted, "unchanged": c.unchanged}
			for k := range want {
				if !reflect.DeepEqual(append([]string(nil), got[k]...), append([]string(nil), want[k]...)) {
					t.Errorf("%s = %v, want %v", k, got[k], want[k])
				}
			}
			var clashes []string
			for _, cf := range diff.Conflicts {
				clashes = append(clashes, cf.Name)
			}
			if !reflect.DeepEqual(clashes, c.clashes) {
				t.Errorf("conflicts = %v, want %v", clashes, c.clashes)
			}
			if len(plan.Create) != len(c.created) || len(plan.Update) != len(c.updated) || len(plan.Delete) != len(c.deleted) {
				t.Errorf("plan sizes %d/%d/%d do not match the diff", len(plan.Create), len(plan.Update), len(plan.Delete))
			}
			seen := map[string]bool{} // an own node is touched at most once
			for _, u := range plan.Update {
				if seen[u.Node.ID] {
					t.Errorf("node %s updated twice", u.Node.ID)
				}
				seen[u.Node.ID] = true
			}
			for _, d := range plan.Delete {
				if seen[d.ID] {
					t.Errorf("node %s updated and deleted", d.ID)
				}
				seen[d.ID] = true
			}
			for _, u := range plan.Update { // an update carries the id and version it read
				if u.Node.ID == "" || u.Expected == 0 {
					t.Errorf("update lacks id/version: %+v", u)
				}
			}
		})
	}
}

func TestPlanSyncIgnoresTheObservedKey(t *testing.T) {
	cur := stored("i1", 4, nd("a", "1"))
	cur.HostKeyObserved = "ssh-ed25519 AAAA"
	_, diff := PlanSync("s1", []store.Node{nd("a", "1")}, []store.Node{cur}, noOthers, false)
	if len(diff.Unchanged) != 1 {
		t.Fatalf("diff = %+v; an observed key must not make a node look changed", diff)
	}
}

// One own node is claimed by one desired node only.
func TestPlanSyncClaimsAnOwnNodeOnce(t *testing.T) {
	own := []store.Node{stored("i1", 1, nd("a", "X"))}
	plan, diff := PlanSync("s1", []store.Node{nd("a", ""), nd("b", "X")}, own, noOthers, false)
	if len(plan.Update) != 1 || plan.Update[0].Node.Name != "b" {
		t.Fatalf("plan = %+v; the node with external id X is renamed to b", plan)
	}
	if len(diff.Conflicts) != 1 || diff.Conflicts[0].Name != "a" {
		t.Fatalf("conflicts = %+v; a is still held by the renamed node", diff.Conflicts)
	}
}
