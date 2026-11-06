package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

// ObserveNodeHostKey implements store.NodeStore.
func (s *Store) ObserveNodeHostKey(ctx context.Context, id, key, attemptID, agentID string, now time.Time) (bool, error) {
	rows, err := s.q.ObserveNodeHostKey(ctx, gen.ObserveNodeHostKeyParams{
		HostKeyObserved: key, ObservedAt: sql.NullString{String: fmtTime(now), Valid: true}, ID: id, AttemptID: attemptID, AgentID: sql.NullString{String: agentID, Valid: true},
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
