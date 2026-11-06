package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

var _ store.RunTargetStore = (*Store)(nil)

// insertRunTargets writes the frozen targets of a new run, in order.
func insertRunTargets(ctx context.Context, q *gen.Queries, runID string, ts []store.RunTarget) error {
	for i, t := range ts {
		err := q.InsertRunTarget(ctx, gen.InsertRunTargetParams{
			RunID: runID, Position: int64(i + 1), Name: t.Name, NodeID: t.NodeID, Node: string(t.Node),
		})
		if err != nil {
			return fmt.Errorf("freezing target %q: %w", t.Name, err)
		}
	}
	return nil
}

// ListRunTargets implements store.RunTargetStore.
func (s *Store) ListRunTargets(ctx context.Context, runID string) ([]store.RunTarget, error) {
	rows, err := s.q.ListRunTargets(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("listing targets of run %q: %w", runID, err)
	}
	out := make([]store.RunTarget, len(rows))
	for i, r := range rows {
		out[i] = store.RunTarget{Position: int(r.Position), Name: r.Name, NodeID: r.NodeID, Node: json.RawMessage(r.Node)}
	}
	return out, nil
}

// ListRunTargetsPage implements store.RunTargetStore.
func (s *Store) ListRunTargetsPage(ctx context.Context, runID string, afterPosition, limit int) ([]store.RunTarget, error) {
	rows, err := s.q.ListRunTargetsPage(ctx, gen.ListRunTargetsPageParams{RunID: runID, AfterPosition: int64(afterPosition), RowLimit: int64(store.ClampTargetLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("listing targets of run %q: %w", runID, err)
	}
	out := make([]store.RunTarget, len(rows))
	for i, r := range rows {
		out[i] = store.RunTarget{Position: int(r.Position), Name: r.Name, NodeID: r.NodeID, Node: json.RawMessage(r.Node)}
	}
	return out, nil
}

// RunTargetNodeID implements store.RunTargetStore.
func (s *Store) RunTargetNodeID(ctx context.Context, runID, name string) (string, error) {
	id, err := s.q.GetRunTargetNodeID(ctx, gen.GetRunTargetNodeIDParams{RunID: runID, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("target %q of run %q: %w", name, runID, store.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("getting target %q of run %q: %w", name, runID, err)
	}
	return id, nil
}
