package sqlite

import (
	"context"
	"fmt"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

// GetNodesByIDs implements store.NodeStore.
func (s *Store) GetNodesByIDs(ctx context.Context, ids []string) (map[string]store.Node, error) {
	out := make(map[string]store.Node, len(ids))
	for _, chunk := range store.Chunks(ids, store.MaxBatch) {
		rows, err := s.q.GetNodesByIDs(ctx, chunk)
		if err != nil {
			return nil, fmt.Errorf("getting nodes by id: %w", err)
		}
		for _, r := range rows {
			n, err := toNode(r)
			if err != nil {
				return nil, err
			}
			out[n.ID] = n
		}
	}
	return out, nil
}

// GetNodesByNames implements store.NodeStore.
func (s *Store) GetNodesByNames(ctx context.Context, names []string) (map[string]store.Node, error) {
	out := make(map[string]store.Node, len(names))
	for _, chunk := range store.Chunks(names, store.MaxBatch) {
		rows, err := s.q.GetNodesByNames(ctx, chunk)
		if err != nil {
			return nil, fmt.Errorf("getting nodes by name: %w", err)
		}
		for _, r := range rows {
			n, err := toNode(r)
			if err != nil {
				return nil, err
			}
			out[n.Name] = n
		}
	}
	return out, nil
}

// RunTargetNodeIDs implements store.RunTargetStore.
func (s *Store) RunTargetNodeIDs(ctx context.Context, runID string, names []string) (map[string]string, error) {
	out := make(map[string]string, len(names))
	for _, chunk := range store.Chunks(names, store.MaxBatch) {
		rows, err := s.q.ListRunTargetNodeIDs(ctx, gen.ListRunTargetNodeIDsParams{RunID: runID, Names: chunk})
		if err != nil {
			return nil, fmt.Errorf("getting targets of run %q: %w", runID, err)
		}
		for _, r := range rows {
			out[r.Name] = r.NodeID
		}
	}
	return out, nil
}
