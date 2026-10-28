package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/denyszorinets/damson/internal/store"
)

var _ store.StampStore = (*Store)(nil)

// GetRunStamp implements store.StampStore.
func (s *Store) GetRunStamp(ctx context.Context, runID string) (store.RunStamp, error) {
	r, err := s.q.GetRunStamp(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.RunStamp{}, fmt.Errorf("run %q: %w", runID, store.ErrNotFound)
	}
	if err != nil {
		return store.RunStamp{}, fmt.Errorf("getting run stamp %q: %w", runID, err)
	}
	return store.RunStamp{
		Version: r.Version, Steps: r.Steps, StepsDone: r.StepsDone, Execs: r.Execs,
		ExecsDone: r.ExecsDone, ExecsRunning: r.ExecsRunning, ExecsDispatched: r.ExecsDispatched,
	}, nil
}

// GetLogStamp implements store.StampStore.
func (s *Store) GetLogStamp(ctx context.Context, executionID string) (store.LogStamp, error) {
	r, err := s.q.GetLogStamp(ctx, executionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.LogStamp{}, fmt.Errorf("execution %q: %w", executionID, store.ErrNotFound)
	}
	if err != nil {
		return store.LogStamp{}, fmt.Errorf("getting log stamp %q: %w", executionID, err)
	}
	return store.LogStamp{State: store.ExecutionState(r.State), Chunks: r.Chunks, MaxSeq: r.MaxSeq}, nil
}
