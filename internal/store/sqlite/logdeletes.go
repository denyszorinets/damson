package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

var _ store.LogDeleteStore = (*Store)(nil)

// queueLogObjectDelete records a deferred delete of the external objects under
// prefix, in the transaction of q.
func queueLogObjectDelete(ctx context.Context, q *gen.Queries, prefix string, notBefore, now time.Time) error {
	if !store.ValidLogDeletePrefix(prefix) {
		return fmt.Errorf("log delete prefix %q: %w", prefix, store.ErrInvalid)
	}
	id, err := store.NewID()
	if err != nil {
		return err
	}
	if err := q.InsertLogObjectDelete(ctx, gen.InsertLogObjectDeleteParams{ID: id, Prefix: prefix, NotBefore: fmtTime(notBefore), CreatedAt: fmtTime(now)}); err != nil {
		return fmt.Errorf("queueing log object delete: %w", err)
	}
	return nil
}

// ListDueLogObjectDeletes implements store.LogDeleteStore.
func (s *Store) ListDueLogObjectDeletes(ctx context.Context, now time.Time, limit int) ([]store.LogObjectDelete, error) {
	rows, err := s.q.ListDueLogObjectDeletes(ctx, gen.ListDueLogObjectDeletesParams{Now: fmtTime(now), RowLimit: int64(store.ClampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("listing due log deletes: %w", err)
	}
	out := make([]store.LogObjectDelete, len(rows))
	for i, r := range rows {
		nb, err := parseTime(r.NotBefore)
		if err != nil {
			return nil, err
		}
		out[i] = store.LogObjectDelete{ID: r.ID, Prefix: r.Prefix, NotBefore: nb, Tries: int(r.Tries)}
	}
	return out, nil
}

// DeferLogObjectDelete implements store.LogDeleteStore.
func (s *Store) DeferLogObjectDelete(ctx context.Context, d store.LogObjectDelete, next time.Time) error {
	n, err := s.q.DeferLogObjectDeleteCAS(ctx, gen.DeferLogObjectDeleteCASParams{ID: d.ID, Next: fmtTime(next), Seen: fmtTime(d.NotBefore)})
	if err != nil {
		return fmt.Errorf("deferring log delete: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("log delete %q: %w", d.ID, store.ErrStale)
	}
	return nil
}

// CompleteLogObjectDelete implements store.LogDeleteStore.
func (s *Store) CompleteLogObjectDelete(ctx context.Context, d store.LogObjectDelete) error {
	n, err := s.q.DeleteLogObjectDeleteCAS(ctx, gen.DeleteLogObjectDeleteCASParams{ID: d.ID, Seen: fmtTime(d.NotBefore)})
	if err != nil {
		return fmt.Errorf("completing log delete: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("log delete %q: %w", d.ID, store.ErrStale)
	}
	return nil
}

// CountLogObjectDeletes implements store.LogDeleteStore.
func (s *Store) CountLogObjectDeletes(ctx context.Context) (int64, error) {
	n, err := s.q.CountLogObjectDeletes(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting log deletes: %w", err)
	}
	return n, nil
}
