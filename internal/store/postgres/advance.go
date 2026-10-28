package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

// staleRun classifies a run CAS that changed no row.
func (s *Store) staleRun(ctx context.Context, runID, what string) error {
	if _, err := s.GetRun(ctx, runID); err != nil {
		return err
	}
	return fmt.Errorf("run %q %s: %w", runID, what, store.ErrStale)
}

// UpdateRunStatus implements store.RunStore.
func (s *Store) UpdateRunStatus(ctx context.Context, runID string, expectedVersion int64, status store.RunStatus, now time.Time) (store.Run, error) {
	cur, err := s.GetRun(ctx, runID)
	if err != nil {
		return store.Run{}, err
	}
	// Stamps come from the pre-read; if another writer interleaves, the
	// version CAS below fails, so they are never applied over a newer row.
	started, finished := store.Stamps(cur.StartedAt, cur.FinishedAt, status == store.RunRunning, status.Terminal(), now)
	n, err := s.q.UpdateRunStatusCAS(ctx, gen.UpdateRunStatusCASParams{
		Status: string(status), StartedAt: tsOpt(started), FinishedAt: tsOpt(finished), ID: runID, ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return store.Run{}, fmt.Errorf("updating run %q: %w", runID, err)
	}
	if n == 0 {
		return store.Run{}, s.staleRun(ctx, runID, fmt.Sprintf("not at version %d", expectedVersion))
	}
	cur.Status, cur.Version, cur.StartedAt, cur.FinishedAt = status, expectedVersion+1, started, finished
	return cur, nil
}

// AddStepInstances implements store.RunStore.
func (s *Store) AddStepInstances(ctx context.Context, runID string, expectedVersion int64, steps []store.NewStepInstance, now time.Time) ([]store.StepNode, error) {
	if err := store.ValidateSteps(steps); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning step add: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	if err := s.bumpRun(ctx, q, runID, expectedVersion); err != nil {
		return nil, err
	}
	nodes, err := insertSteps(ctx, q, runID, steps, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing step add: %w", err)
	}
	return nodes, nil
}

func (s *Store) bumpRun(ctx context.Context, q *gen.Queries, runID string, expectedVersion int64) error {
	n, err := q.BumpRunVersionCAS(ctx, gen.BumpRunVersionCASParams{ID: runID, ExpectedVersion: expectedVersion})
	if err != nil {
		return fmt.Errorf("bumping run %q: %w", runID, err)
	}
	if n == 0 {
		return s.staleRun(ctx, runID, fmt.Sprintf("not at version %d", expectedVersion))
	}
	return nil
}

// ScheduleExecution implements store.RunStore.
func (s *Store) ScheduleExecution(ctx context.Context, runID string, expectedVersion int64, stepInstanceID string,
	ne store.NewExecution, now time.Time) (store.Execution, error) {
	if err := store.ValidateNewExecution(ne); err != nil {
		return store.Execution{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return store.Execution{}, fmt.Errorf("beginning schedule: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	if err := s.bumpRun(ctx, q, runID, expectedVersion); err != nil {
		return store.Execution{}, err
	}
	step, err := q.GetStepInstance(ctx, stepInstanceID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && step.RunID != runID) {
		return store.Execution{}, fmt.Errorf("step %q in run %q: %w", stepInstanceID, runID, store.ErrNotFound)
	}
	if err != nil {
		return store.Execution{}, fmt.Errorf("getting step %q: %w", stepInstanceID, err)
	}
	last, err := q.LatestAttempt(ctx, stepInstanceID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return store.Execution{}, fmt.Errorf("reading latest attempt of step %q: %w", stepInstanceID, err)
	}
	if err == nil && !store.ExecutionState(last.State).Terminal() {
		return store.Execution{}, fmt.Errorf("step %q attempt %d is still %s: %w", stepInstanceID, last.AttemptNo, last.State, store.ErrConflict)
	}
	ex, err := insertExecution(ctx, q, runID, stepInstanceID, int(last.AttemptNo)+1, ne, now)
	if err != nil {
		return store.Execution{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Execution{}, fmt.Errorf("committing schedule: %w", err)
	}
	return ex, nil
}

// UpdateStepInstance implements store.RunStore.
func (s *Store) UpdateStepInstance(ctx context.Context, u store.StepUpdate, now time.Time) (store.StepInstance, error) {
	outputs, err := store.JSONOrEmpty(u.Outputs)
	if err != nil {
		return store.StepInstance{}, err
	}
	cur, err := s.GetStepInstance(ctx, u.ID)
	if err != nil {
		return store.StepInstance{}, err
	}
	started, finished := store.Stamps(cur.StartedAt, cur.FinishedAt, u.Status == store.StepRunning, u.Status.Terminal(), now)
	n, err := s.q.UpdateStepInstanceCAS(ctx, gen.UpdateStepInstanceCASParams{
		Status: string(u.Status), Outputs: string(outputs), Error: u.Error, StartedAt: tsOpt(started), FinishedAt: tsOpt(finished),
		ID: u.ID, ExpectedStatus: string(u.ExpectedStatus),
	})
	if err != nil {
		return store.StepInstance{}, fmt.Errorf("updating step %q: %w", u.ID, err)
	}
	if n == 0 {
		return store.StepInstance{}, fmt.Errorf("step %q not in status %q: %w", u.ID, u.ExpectedStatus, store.ErrStale)
	}
	cur.Status, cur.Outputs, cur.Error, cur.StartedAt, cur.FinishedAt = u.Status, json.RawMessage(outputs), u.Error, started, finished
	return cur, nil
}

// RequestCancel implements store.RunStore.
func (s *Store) RequestCancel(ctx context.Context, runID string, now time.Time) (store.Run, error) {
	n, err := s.q.RequestRunCancelCAS(ctx, gen.RequestRunCancelCASParams{Now: ts(now), ID: runID})
	if err != nil {
		return store.Run{}, fmt.Errorf("requesting cancel of run %q: %w", runID, err)
	}
	if n == 0 {
		return store.Run{}, s.staleRun(ctx, runID, "cancel already requested or run finished")
	}
	return s.GetRun(ctx, runID)
}
