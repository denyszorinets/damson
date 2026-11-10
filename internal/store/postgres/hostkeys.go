package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

// ObserveNodeHostKey implements store.NodeStore.
func (s *Store) ObserveNodeHostKey(ctx context.Context, id, key, attemptID, agentID string, now time.Time) (bool, error) {
	rows, err := s.q.ObserveNodeHostKey(ctx, gen.ObserveNodeHostKeyParams{
		HostKeyObserved: key, ObservedAt: pgtype.Timestamptz{Time: store.NormalizeTime(now), Valid: true}, ID: id, AttemptID: attemptID, AgentID: pgtype.Text{String: agentID, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("recording the host key of node %q: %w", id, err)
	}
	return rows > 0, nil
}

// ClearNodeHostKeyObserved implements store.NodeStore.
func (s *Store) ClearNodeHostKeyObserved(ctx context.Context, id string) error {
	rows, err := s.q.ClearNodeHostKeyObserved(ctx, id)
	if err != nil {
		return fmt.Errorf("clearing the observed host key of node %q: %w", id, err)
	}
	if rows == 0 {
		if _, err := s.GetNodeByID(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
