package inventory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// Conflict is a desired node that cannot be written: its name belongs to a
// node of another source.
type Conflict struct {
	Name    string
	OwnedBy string
}

// Diff is what a sync does (or would do, for a dry run), by node name.
type Diff struct {
	Created, Updated, Deleted, Unchanged []string
	Conflicts                            []Conflict
}

// ErrSyncConflict means a desired name is held by a node of another source, or
// by an own node that stays; nothing was written.
var ErrSyncConflict = errors.New("sync conflicts with existing node names")

// ErrBadDesired means the desired set itself is invalid (a node that fails
// validation, or duplicate names or external ids).
var ErrBadDesired = errors.New("invalid desired node set")

// maxSyncAttempts bounds re-planning when a concurrent writer moved a node.
const maxSyncAttempts = 3

// PlanSync computes the changes that make the nodes of source equal to
// desired. own is every node of the source; others returns the node holding a
// name in another source, if any. desired must be validated and normalized
// (Validate) with Source set to source. It is pure: no I/O.
//
// A desired node matches an own node by external_id, else by name (adopting an
// own node that has no external id yet). A node with a changed name is a
// rename. With prune, own nodes that match nothing are deleted. Unchanged
// nodes produce no write, so their version stays. The observed host key is
// never part of the comparison.
func PlanSync(source string, desired, own []store.Node, others func(name string) (owner string, ok bool), prune bool) (store.NodeDiff, Diff) {
	var plan store.NodeDiff
	var diff Diff
	byExt := map[string]store.Node{}
	byName := map[string]store.Node{}
	for _, n := range own {
		if n.ExternalID != "" {
			byExt[n.ExternalID] = n
		}
		byName[strings.ToLower(n.Name)] = n
	}
	// Two passes so an own node is claimed once: external ids first, then names
	// among the nodes still free.
	cur := make([]*store.Node, len(desired))
	claimed := map[string]bool{}
	for i, d := range desired {
		if n, ok := byExt[d.ExternalID]; ok && d.ExternalID != "" {
			cur[i] = &n
			claimed[n.ID] = true
		}
	}
	for i, d := range desired {
		if cur[i] != nil {
			continue
		}
		n, ok := byName[strings.ToLower(d.Name)]
		if !ok || claimed[n.ID] || (n.ExternalID != "" && d.ExternalID != "" && n.ExternalID != d.ExternalID) {
			continue
		}
		cur[i] = &n
		claimed[n.ID] = true
	}
	// A name is free if no other source holds it and no own node holds it, unless
	// that node is the one being renamed or is pruned. Chains and swaps of names
	// are therefore conflicts: the store applies one statement at a time.
	conflictFor := func(name, selfID string) (Conflict, bool) {
		if owner, ok := others(name); ok {
			return Conflict{Name: name, OwnedBy: owner}, true
		}
		if held, ok := byName[strings.ToLower(name)]; ok && held.ID != selfID && (!prune || claimed[held.ID]) {
			return Conflict{Name: name, OwnedBy: source}, true
		}
		return Conflict{}, false
	}
	for i, d := range desired {
		switch c := cur[i]; {
		case c == nil:
			if cf, bad := conflictFor(d.Name, ""); bad {
				diff.Conflicts = append(diff.Conflicts, cf)
				continue
			}
			plan.Create = append(plan.Create, d)
			diff.Created = append(diff.Created, d.Name)
		case sameNode(*c, d):
			diff.Unchanged = append(diff.Unchanged, d.Name)
		default:
			if !strings.EqualFold(c.Name, d.Name) {
				if cf, bad := conflictFor(d.Name, c.ID); bad {
					diff.Conflicts = append(diff.Conflicts, cf)
					continue
				}
			}
			d.ID, d.CreatedAt = c.ID, c.CreatedAt
			plan.Update = append(plan.Update, store.NodeUpdate{Node: d, Expected: c.Version})
			diff.Updated = append(diff.Updated, d.Name)
		}
	}
	if prune {
		for _, n := range own {
			if !claimed[n.ID] {
				plan.Delete = append(plan.Delete, store.NodeDelete{ID: n.ID, Expected: n.Version})
				diff.Deleted = append(diff.Deleted, n.Name)
			}
		}
	}
	return plan, diff
}

// sameNode compares the fields a source owns, in their normalized form. The
// observed host key and the version are not part of it.
func sameNode(cur, d store.Node) bool {
	return cur.Name == d.Name && cur.ExternalID == d.ExternalID && cur.Address == d.Address && cur.Port == d.Port &&
		cur.Transport == d.Transport && cur.OS == d.OS && cur.Pool == d.Pool && cur.Credential == d.Credential &&
		cur.HostKey == d.HostKey && cur.HostKeyPolicy == d.HostKeyPolicy && cur.Enabled == d.Enabled &&
		reflect.DeepEqual(nonNilTags(cur.Tags), nonNilTags(d.Tags)) && reflect.DeepEqual(nonNilAttrs(cur.Attributes), nonNilAttrs(d.Attributes))
}

func nonNilTags(t []string) []string {
	if t == nil {
		return []string{}
	}
	return t
}

func nonNilAttrs(a map[string]any) map[string]any {
	if a == nil {
		return map[string]any{}
	}
	return a
}

// Sync makes the nodes of source equal to desired in one transaction. With
// dryRun it only reports. Conflicts with other sources fail the whole call
// before anything is written (ErrSyncConflict, the Diff lists them). A node
// that moves between the read and the write re-plans, up to three times.
func Sync(ctx context.Context, st store.NodeStore, source string, desired []store.Node, prune, dryRun bool, now time.Time) (Diff, error) {
	return run(ctx, st, source, desired, nil, prune, dryRun, now)
}

// DeleteRef names a node of the source to delete, by external id or, without
// one, by name. A node that is not there is ignored, so a delete can be repeated.
type DeleteRef struct {
	ExternalID string
	Name       string
}

// Batch applies incremental changes to the nodes of source: upserts as Sync
// does but never pruning, and deletes of that source's nodes. A node cannot be
// upserted and deleted in the same call (ErrBadDesired).
func Batch(ctx context.Context, st store.NodeStore, source string, upserts []store.Node, deletes []DeleteRef, dryRun bool, now time.Time) (Diff, error) {
	return run(ctx, st, source, upserts, deletes, false, dryRun, now)
}

func run(ctx context.Context, st store.NodeStore, source string, desired []store.Node, deletes []DeleteRef, prune, dryRun bool, now time.Time) (Diff, error) {
	nodes, err := prepare(source, desired)
	if err != nil {
		return Diff{}, err
	}
	var lastErr error
	for range maxSyncAttempts {
		own, err := listSource(ctx, st, source)
		if err != nil {
			return Diff{}, err
		}
		others, err := ownerLookup(ctx, st, source, nodes, own)
		if err != nil {
			return Diff{}, err
		}
		dropped, err := resolveDeletes(own, nodes, deletes)
		if err != nil {
			return Diff{}, err
		}
		// The deleted nodes are planned away first, so the names they held are free.
		plan, diff := PlanSync(source, nodes, withoutNodes(own, dropped), others, prune)
		for _, n := range dropped {
			plan.Delete = append(plan.Delete, store.NodeDelete{ID: n.ID, Expected: n.Version})
			diff.Deleted = append(diff.Deleted, n.Name)
		}
		if len(diff.Conflicts) > 0 {
			return diff, fmt.Errorf("%w: %d node(s)", ErrSyncConflict, len(diff.Conflicts))
		}
		if dryRun || len(plan.Create)+len(plan.Update)+len(plan.Delete) == 0 {
			return diff, nil // a replay writes nothing, not even an empty transaction
		}
		if err := assignIDs(&plan); err != nil {
			return Diff{}, err
		}
		sortPlan(&plan)
		err = st.ApplyNodeDiff(ctx, plan, now)
		if err == nil {
			return diff, nil
		}
		if !errors.Is(err, store.ErrStale) && !errors.Is(err, store.ErrConflict) {
			return Diff{}, err
		}
		lastErr = err
	}
	return Diff{}, fmt.Errorf("syncing source %q: %w", source, lastErr)
}

// resolveDeletes picks the own nodes a batch deletes. A ref matches by external
// id, else by name; one that matches nothing is ignored (a delete can be
// repeated). A ref that names the identity of an upsert (the same external id,
// or the same name when it has no external id) is refused, so a node is never
// upserted and deleted in one call. Re-using the name of a deleted node for a
// node with another external id is fine: the delete frees the name.
func resolveDeletes(own, upserts []store.Node, deletes []DeleteRef) ([]store.Node, error) {
	if len(deletes) == 0 {
		return nil, nil
	}
	upExt, upName := map[string]bool{}, map[string]bool{}
	for _, u := range upserts {
		if u.ExternalID != "" {
			upExt[u.ExternalID] = true
		}
		upName[strings.ToLower(u.Name)] = true
	}
	byExt, byName := map[string]store.Node{}, map[string]store.Node{}
	for _, n := range own {
		if n.ExternalID != "" {
			byExt[n.ExternalID] = n
		}
		byName[strings.ToLower(n.Name)] = n
	}
	seen := map[string]bool{}
	var out []store.Node
	for _, d := range deletes {
		var n store.Node
		var found bool
		switch {
		case d.ExternalID != "":
			if upExt[d.ExternalID] {
				return nil, fmt.Errorf("%w: external id %q is both upserted and deleted", ErrBadDesired, d.ExternalID)
			}
			n, found = byExt[d.ExternalID]
		case d.Name != "":
			if upName[strings.ToLower(d.Name)] {
				return nil, fmt.Errorf("%w: %q is both upserted and deleted", ErrBadDesired, d.Name)
			}
			n, found = byName[strings.ToLower(d.Name)]
		default:
			return nil, fmt.Errorf("%w: a delete needs an external id or a name", ErrBadDesired)
		}
		if !found || seen[n.ID] {
			continue
		}
		// An upsert with the deleted node's external id would match it too.
		if n.ExternalID != "" && upExt[n.ExternalID] {
			return nil, fmt.Errorf("%w: %q is both upserted and deleted", ErrBadDesired, n.Name)
		}
		seen[n.ID] = true
		out = append(out, n)
	}
	return out, nil
}

// withoutNodes returns own minus the given nodes.
func withoutNodes(own, drop []store.Node) []store.Node {
	if len(drop) == 0 {
		return own
	}
	gone := make(map[string]bool, len(drop))
	for _, n := range drop {
		gone[n.ID] = true
	}
	kept := make([]store.Node, 0, len(own))
	for _, n := range own {
		if !gone[n.ID] {
			kept = append(kept, n)
		}
	}
	return kept
}

// prepare validates and normalizes the desired set and rejects duplicates.
func prepare(source string, desired []store.Node) ([]store.Node, error) {
	if source == SourceManual {
		return nil, fmt.Errorf("%w: the manual source belongs to the nodes API", ErrBadDesired)
	}
	out := make([]store.Node, len(desired))
	names, exts := map[string]bool{}, map[string]bool{}
	for i, d := range desired {
		d.Source = source
		if is := Validate(&d); len(is) > 0 {
			return nil, fmt.Errorf("%w: node %q: %s: %s", ErrBadDesired, d.Name, is[0].Field, is[0].Message)
		}
		lower := strings.ToLower(d.Name)
		if names[lower] {
			return nil, fmt.Errorf("%w: name %q appears twice", ErrBadDesired, d.Name)
		}
		names[lower] = true
		if d.ExternalID != "" {
			if exts[d.ExternalID] {
				return nil, fmt.Errorf("%w: external id of %q appears twice", ErrBadDesired, d.Name)
			}
			exts[d.ExternalID] = true
		}
		out[i] = d
	}
	return out, nil
}

func listSource(ctx context.Context, st store.NodeStore, source string) ([]store.Node, error) {
	var all []store.Node
	after := ""
	for {
		page, err := st.ListNodes(ctx, store.NodeFilter{Source: source, AfterName: after, Limit: 1000})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < 1000 {
			return all, nil
		}
		after = page[len(page)-1].Name
	}
}

// ownerLookup finds, for each desired name, a node of another source holding
// it (ignoring case, as the unique index does). Names held by an own node
// cannot be held by another source, so a replay costs no lookups.
func ownerLookup(ctx context.Context, st store.NodeStore, source string, desired, own []store.Node) (func(string) (string, bool), error) {
	ownNames := map[string]bool{}
	for _, n := range own {
		ownNames[strings.ToLower(n.Name)] = true
	}
	owners := map[string]string{}
	for _, d := range desired {
		lower := strings.ToLower(d.Name)
		if ownNames[lower] {
			continue
		}
		n, err := st.FindNodeByName(ctx, d.Name)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n.Source != source {
			owners[lower] = n.Source
		}
	}
	return func(name string) (string, bool) {
		o, ok := owners[strings.ToLower(name)]
		return o, ok
	}, nil
}

// sortPlan orders updates and deletes by id, so concurrent syncs of the same
// source take row locks in the same order.
func sortPlan(p *store.NodeDiff) {
	sort.Slice(p.Update, func(i, j int) bool { return p.Update[i].Node.ID < p.Update[j].Node.ID })
	sort.Slice(p.Delete, func(i, j int) bool { return p.Delete[i].ID < p.Delete[j].ID })
}

func assignIDs(plan *store.NodeDiff) error {
	for i := range plan.Create {
		id, err := store.NewID()
		if err != nil {
			return err
		}
		plan.Create[i].ID = id
	}
	return nil
}
