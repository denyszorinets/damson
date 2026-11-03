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

var _ store.LeaderStore = (*Store)(nil)

// AcquireLeader implements store.LeaderStore.
func (s *Store) AcquireLeader(ctx context.Context, name, holder string, now, expiresAt time.Time) (store.Lease, bool, error) {
	row, err := s.q.AcquireLeaderLease(ctx, gen.AcquireLeaderLeaseParams{Name: name, Holder: holder, Now: fmtTime(now), ExpiresAt: fmtTime(expiresAt)})
	if errors.Is(err, sql.ErrNoRows) {
		return store.Lease{}, false, nil
	}
	if err != nil {
		return store.Lease{}, false, fmt.Errorf("acquiring leader lease %q: %w", name, err)
	}
	exp, err := parseTime(row.ExpiresAt)
	if err != nil {
		return store.Lease{}, false, fmt.Errorf("reading leader lease %q: %w", name, err)
	}
	return store.Lease{Term: row.Term, ExpiresAt: exp}, true, nil
}

// ReleaseLeader implements store.LeaderStore.
func (s *Store) ReleaseLeader(ctx context.Context, name, holder string, term int64, now time.Time) error {
	if _, err := s.q.ReleaseLeaderLease(ctx, gen.ReleaseLeaderLeaseParams{Now: fmtTime(now), Name: name, Holder: holder, Term: term}); err != nil {
		return fmt.Errorf("releasing leader lease %q: %w", name, err)
	}
	return nil
}

// DeleteDeadControllerInstances implements store.LeaderStore.
func (s *Store) DeleteDeadControllerInstances(ctx context.Context, cutoff time.Time) (int64, error) {
	n, err := s.q.DeleteDeadControllerInstances(ctx, fmtTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("deleting dead controller instances: %w", err)
	}
	return n, nil
}

// CheckLeader implements store.Tx.
func (t *txn) CheckLeader(ctx context.Context, name, holder string, term int64, now time.Time) error {
	n, err := t.q.CheckLeaderLease(ctx, gen.CheckLeaderLeaseParams{Now: fmtTime(now), Name: name, Holder: holder, Term: term})
	if err != nil {
		return fmt.Errorf("checking leader lease %q: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("leader lease %q term %d of %q: %w", name, term, holder, store.ErrStale)
	}
	return nil
}
