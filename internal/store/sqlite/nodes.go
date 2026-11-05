package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

var _ store.NodeStore = (*Store)(nil)

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func toNode(r gen.Node) (store.Node, error) {
	tags, err := store.DecodeTags(r.Tags)
	if err != nil {
		return store.Node{}, err
	}
	attrs, err := store.DecodeAttributes(r.Attributes)
	if err != nil {
		return store.Node{}, err
	}
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return store.Node{}, err
	}
	updated, err := parseTime(r.UpdatedAt)
	if err != nil {
		return store.Node{}, err
	}
	n := store.Node{
		ID: r.ID, Name: r.Name, Address: r.Address, Port: int(r.Port), Transport: r.Transport, OS: r.Os, Pool: r.Pool,
		Tags: tags, Attributes: attrs, Credential: r.Credential, HostKey: r.HostKey, HostKeyPolicy: r.HostKeyPolicy,
		HostKeyObserved: r.HostKeyObserved, Source: r.Source, ExternalID: r.ExternalID, Enabled: r.Enabled == 1,
		Version: r.Version, CreatedAt: created, UpdatedAt: updated,
	}
	if r.HostKeyObservedAt.Valid {
		t, err := parseTime(r.HostKeyObservedAt.String)
		if err != nil {
			return store.Node{}, err
		}
		n.HostKeyObservedAt = &t
	}
	return n, nil
}

// CreateNode implements store.NodeStore.
func (s *Store) CreateNode(ctx context.Context, n store.Node, now time.Time) (store.Node, error) {
	tags, err := store.EncodeTags(n.Tags)
	if err != nil {
		return store.Node{}, err
	}
	attrs, err := store.EncodeAttributes(n.Attributes)
	if err != nil {
		return store.Node{}, err
	}
	err = s.q.InsertNode(ctx, insertNodeParams(n, tags, attrs, now))
	if isUnique(err) {
		return store.Node{}, fmt.Errorf("node %q: %w", n.Name, store.ErrConflict)
	}
	if err != nil {
		return store.Node{}, fmt.Errorf("creating node %q: %w", n.Name, err)
	}
	return s.getNodeByID(ctx, n.ID)
}

func (s *Store) getNodeByID(ctx context.Context, id string) (store.Node, error) {
	r, err := s.q.GetNodeByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Node{}, fmt.Errorf("node %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Node{}, fmt.Errorf("getting node %q: %w", id, err)
	}
	return toNode(r)
}

// GetNode implements store.NodeStore.
func (s *Store) GetNode(ctx context.Context, name string) (store.Node, error) {
	r, err := s.q.GetNodeByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Node{}, fmt.Errorf("node %q: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return store.Node{}, fmt.Errorf("getting node %q: %w", name, err)
	}
	return toNode(r)
}

// FindNodeByName implements store.NodeStore.
func (s *Store) FindNodeByName(ctx context.Context, name string) (store.Node, error) {
	r, err := s.q.GetNodeByLowerName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Node{}, fmt.Errorf("node %q: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return store.Node{}, fmt.Errorf("finding node %q: %w", name, err)
	}
	return toNode(r)
}

// ListNodes implements store.NodeStore.
func (s *Store) ListNodes(ctx context.Context, f store.NodeFilter) ([]store.Node, error) {
	p := gen.ListNodesParams{AfterName: f.AfterName, Source: f.Source, AnyEnabled: 1, RowLimit: int64(store.ClampNodeLimit(f.Limit))}
	if f.Enabled != nil {
		p.AnyEnabled, p.Enabled = 0, b2i(*f.Enabled)
	}
	rows, err := s.q.ListNodes(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	out := make([]store.Node, 0, len(rows))
	for _, r := range rows {
		n, err := toNode(r)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// UpdateNode implements store.NodeStore.
func (s *Store) UpdateNode(ctx context.Context, n store.Node, expected int64, now time.Time) (store.Node, error) {
	tags, err := store.EncodeTags(n.Tags)
	if err != nil {
		return store.Node{}, err
	}
	attrs, err := store.EncodeAttributes(n.Attributes)
	if err != nil {
		return store.Node{}, err
	}
	rows, err := s.q.UpdateNodeCAS(ctx, gen.UpdateNodeCASParams{
		Address: n.Address, Port: int64(n.Port), Transport: n.Transport, Os: n.OS, Pool: n.Pool, Tags: tags, Attributes: attrs,
		Credential: n.Credential, HostKey: n.HostKey, HostKeyPolicy: n.HostKeyPolicy, Enabled: b2i(n.Enabled),
		UpdatedAt: fmtTime(now), ID: n.ID, ExpectedVersion: expected,
	})
	if err != nil {
		return store.Node{}, fmt.Errorf("updating node %q: %w", n.ID, err)
	}
	if rows == 0 {
		return store.Node{}, s.casMiss(ctx, n.ID, expected)
	}
	return s.getNodeByID(ctx, n.ID)
}

// DeleteNode implements store.NodeStore.
func (s *Store) DeleteNode(ctx context.Context, id string, expected int64) error {
	rows, err := s.q.DeleteNodeCAS(ctx, gen.DeleteNodeCASParams{ID: id, ExpectedVersion: expected})
	if err != nil {
		return fmt.Errorf("deleting node %q: %w", id, err)
	}
	if rows == 0 {
		return s.casMiss(ctx, id, expected)
	}
	return nil
}

// casMiss tells a missing node from a version that moved.
func (s *Store) casMiss(ctx context.Context, id string, expected int64) error {
	if _, err := s.getNodeByID(ctx, id); err != nil {
		return err
	}
	return fmt.Errorf("node %q at version %d: %w", id, expected, store.ErrStale)
}

func insertNodeParams(n store.Node, tags, attrs string, now time.Time) gen.InsertNodeParams {
	return gen.InsertNodeParams{
		ID: n.ID, Name: n.Name, Address: n.Address, Port: int64(n.Port), Transport: n.Transport, Os: n.OS, Pool: n.Pool,
		Tags: tags, Attributes: attrs, Credential: n.Credential, HostKey: n.HostKey, HostKeyPolicy: n.HostKeyPolicy,
		Source: n.Source, ExternalID: n.ExternalID, Enabled: b2i(n.Enabled), CreatedAt: fmtTime(now), UpdatedAt: fmtTime(now),
	}
}

// ApplyNodeDiff implements store.NodeStore.
func (s *Store) ApplyNodeDiff(ctx context.Context, d store.NodeDiff, now time.Time) error {
	return s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		for _, del := range d.Delete {
			rows, err := q.DeleteNodeCAS(ctx, gen.DeleteNodeCASParams{ID: del.ID, ExpectedVersion: del.Expected})
			if err != nil {
				return fmt.Errorf("deleting node %q: %w", del.ID, err)
			}
			if rows == 0 {
				return fmt.Errorf("node %q at version %d: %w", del.ID, del.Expected, store.ErrStale)
			}
		}
		for _, u := range d.Update {
			n := u.Node
			tags, err := store.EncodeTags(n.Tags)
			if err != nil {
				return err
			}
			attrs, err := store.EncodeAttributes(n.Attributes)
			if err != nil {
				return err
			}
			rows, err := q.UpdateNodeSyncCAS(ctx, gen.UpdateNodeSyncCASParams{
				Name: n.Name, ExternalID: n.ExternalID, Address: n.Address, Port: int64(n.Port), Transport: n.Transport,
				Os: n.OS, Pool: n.Pool, Tags: tags, Attributes: attrs, Credential: n.Credential, HostKey: n.HostKey,
				HostKeyPolicy: n.HostKeyPolicy, Enabled: b2i(n.Enabled), UpdatedAt: fmtTime(now), ID: n.ID, ExpectedVersion: u.Expected,
			})
			if isUnique(err) {
				return fmt.Errorf("node %q: %w", n.Name, store.ErrConflict)
			}
			if err != nil {
				return fmt.Errorf("updating node %q: %w", n.ID, err)
			}
			if rows == 0 {
				return fmt.Errorf("node %q at version %d: %w", n.ID, u.Expected, store.ErrStale)
			}
		}
		for _, n := range d.Create {
			tags, err := store.EncodeTags(n.Tags)
			if err != nil {
				return err
			}
			attrs, err := store.EncodeAttributes(n.Attributes)
			if err != nil {
				return err
			}
			err = q.InsertNode(ctx, insertNodeParams(n, tags, attrs, now))
			if isUnique(err) {
				return fmt.Errorf("node %q: %w", n.Name, store.ErrConflict)
			}
			if err != nil {
				return fmt.Errorf("creating node %q: %w", n.Name, err)
			}
		}
		return nil
	})
}
