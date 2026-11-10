package inventory

import (
	"errors"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
)

func TestResolveDeletes(t *testing.T) {
	own := []store.Node{stored("id-a", 1, nd("a", "1")), stored("id-b", 2, nd("b", "2")), stored("id-c", 3, nd("c", ""))}
	names := func(ns []store.Node) []string {
		var out []string
		for _, n := range ns {
			out = append(out, n.Name)
		}
		return out
	}
	ok := []struct {
		name    string
		ups     []store.Node
		dels    []DeleteRef
		deleted []string
	}{
		{"by external id", nil, []DeleteRef{{ExternalID: "1"}}, []string{"a"}},
		{"by name, any case", nil, []DeleteRef{{Name: "B"}}, []string{"b"}},
		{"a node without external id, by name", nil, []DeleteRef{{Name: "c"}}, []string{"c"}},
		{"two refs to one node count once", nil, []DeleteRef{{ExternalID: "1"}, {ExternalID: "1"}}, []string{"a"}},
		{"unknown refs are ignored", nil, []DeleteRef{{ExternalID: "zz"}, {Name: "nope"}}, nil},
		{"the external id decides when both are given", nil, []DeleteRef{{ExternalID: "zz", Name: "a"}}, nil},
		{"re-identification: the delete frees the name", []store.Node{nd("b", "9")}, []DeleteRef{{ExternalID: "2"}}, []string{"b"}},
		{"a name that looks like an id is just a name", nil, []DeleteRef{{Name: "id-a"}}, nil},
		{"delete of another node while one is upserted", []store.Node{nd("a", "1")}, []DeleteRef{{ExternalID: "2"}}, []string{"b"}},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveDeletes(own, c.ups, c.dels)
			if err != nil {
				t.Fatal(err)
			}
			if g := names(got); len(g) != len(c.deleted) || (len(g) > 0 && g[0] != c.deleted[0]) {
				t.Fatalf("deleted %v, want %v", g, c.deleted)
			}
		})
	}
	bad := []struct {
		name string
		ups  []store.Node
		dels []DeleteRef
	}{
		{"empty ref", nil, []DeleteRef{{}}},
		{"external id of an upsert that creates", []store.Node{nd("x", "9")}, []DeleteRef{{ExternalID: "9"}}},
		{"name of an upsert that creates", []store.Node{nd("x", "")}, []DeleteRef{{Name: "x"}}},
		{"external id of an upsert that updates", []store.Node{nd("a", "1")}, []DeleteRef{{ExternalID: "1"}}},
		{"the new name of a rename", []store.Node{nd("a2", "1")}, []DeleteRef{{Name: "a2"}}},
		{"the old name of a rename by external id", []store.Node{nd("a2", "1")}, []DeleteRef{{Name: "a"}, {ExternalID: "1"}}},
		{"a node an upsert re-uses by external id", []store.Node{nd("zz", "2")}, []DeleteRef{{Name: "b"}}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := resolveDeletes(own, c.ups, c.dels); !errors.Is(err, ErrBadDesired) {
				t.Fatalf("err = %v; want ErrBadDesired", err)
			}
		})
	}
}
