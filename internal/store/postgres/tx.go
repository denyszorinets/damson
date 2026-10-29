package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

var _ store.TxRunner = (*Store)(nil)

// Pool returns the connection pool, for the River queue client (ADR-0010).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// WithTx implements store.TxRunner.
func (s *Store) WithTx(ctx context.Context, fn func(tx store.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	// Rollback after a successful commit is a no-op, so it also covers panics.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(&txn{tx: tx, q: s.q.WithTx(tx)}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}

type txn struct {
	tx pgx.Tx
	q  *gen.Queries
}

func (t *txn) GetRun(ctx context.Context, runID string) (store.Run, error) {
	return getRun(ctx, t.q, runID)
}

func (t *txn) TouchRun(ctx context.Context, runID string) (int64, error) {
	n, err := t.q.TouchRunCAS(ctx, runID)
	if err != nil {
		return 0, fmt.Errorf("touching run %q: %w", runID, err)
	}
	if n == 0 {
		return 0, staleRun(ctx, t.q, runID, "is finished")
	}
	run, err := getRun(ctx, t.q, runID)
	return run.Version, err
}

func (t *txn) Native() any { return t.tx }

func (t *txn) CreateRun(ctx context.Context, nr store.NewRun, now time.Time) (store.RunTree, error) {
	return createRun(ctx, t.q, nr, now)
}

func (t *txn) RequestCancel(ctx context.Context, runID string, now time.Time) (store.Run, error) {
	n, err := t.q.RequestRunCancelCAS(ctx, gen.RequestRunCancelCASParams{Now: ts(now), ID: runID})
	if err != nil {
		return store.Run{}, fmt.Errorf("requesting cancel of run %q: %w", runID, err)
	}
	if n == 0 {
		return store.Run{}, staleRun(ctx, t.q, runID, "cancel already requested or run finished")
	}
	return getRun(ctx, t.q, runID)
}

func (t *txn) ApplyAdvance(ctx context.Context, runID string, expectedVersion int64, a store.Advance, now time.Time) (store.Run, error) {
	if err := store.ValidateAdvance(a); err != nil {
		return store.Run{}, err
	}
	cur, err := getRun(ctx, t.q, runID)
	if err != nil {
		return store.Run{}, err
	}
	if a.Empty() {
		return cur, nil
	}
	if err := t.bumpOrMove(ctx, cur, expectedVersion, a, now); err != nil {
		return store.Run{}, err
	}
	if err := t.applySteps(ctx, runID, a, now); err != nil {
		return store.Run{}, err
	}
	return getRun(ctx, t.q, runID)
}

// bumpOrMove is the one CAS of the advance: it checks runs.version and, in
// the same statement, bumps it (and moves the status when Start or Finish ask).
func (t *txn) bumpOrMove(ctx context.Context, cur store.Run, expectedVersion int64, a store.Advance, now time.Time) error {
	status, started, finished, changed := store.AdvanceRunStatus(cur, a, now)
	if !changed {
		return bumpRun(ctx, t.q, cur.ID, expectedVersion)
	}
	n, err := t.q.UpdateRunStatusCAS(ctx, gen.UpdateRunStatusCASParams{
		Status: string(status), StartedAt: tsOpt(started), FinishedAt: tsOpt(finished), ID: cur.ID, ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return fmt.Errorf("updating run %q: %w", cur.ID, err)
	}
	if n == 0 {
		return staleRun(ctx, t.q, cur.ID, fmt.Sprintf("not at version %d", expectedVersion))
	}
	return nil
}

func (t *txn) applySteps(ctx context.Context, runID string, a store.Advance, now time.Time) error {
	for _, st := range a.NewSteps {
		if err := t.addStep(ctx, runID, st, now); err != nil {
			return err
		}
	}
	for _, at := range a.NewAttempts {
		step, err := t.q.GetStepInstanceByPath(ctx, gen.GetStepInstanceByPathParams{RunID: runID, Path: at.Path})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("step %q in run %q: %w", at.Path, runID, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("getting step %q: %w", at.Path, err)
		}
		if _, err := t.q.LatestAttempt(ctx, step.ID); err == nil {
			return fmt.Errorf("step %q already has an attempt: %w", at.Path, store.ErrConflict)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("reading latest attempt of step %q: %w", at.Path, err)
		}
		if _, err := insertExecution(ctx, t.q, runID, step.ID, 1, at.NewExecution, now); err != nil {
			return err
		}
	}
	for _, id := range a.CancelQueued {
		if err := t.cancelQueued(ctx, runID, id, now); err != nil {
			return err
		}
	}
	return nil
}

// addStep inserts the instance pending and, for the finished statuses, moves
// it to its final status in the same transaction.
func (t *txn) addStep(ctx context.Context, runID string, st store.AdvanceStep, now time.Time) error {
	ns := store.NewStepInstance{Path: st.Path, Plugin: st.Plugin, Input: st.Input, Execution: st.Execution}
	nodes, err := insertSteps(ctx, t.q, runID, []store.NewStepInstance{ns}, now)
	if err != nil {
		return err
	}
	if status := st.StatusOrDefault(); status != store.StepPending {
		return t.finishStep(ctx, nodes[0].ID, status, st.Reason, now)
	}
	return nil
}

func (t *txn) finishStep(ctx context.Context, stepID string, status store.StepStatus, reason string, now time.Time) error {
	return t.finishStepFrom(ctx, stepID, status, reason, now, store.StepPending)
}

// finishStepFrom moves a step instance to a terminal status if it is in any of
// the expected statuses. A miss is a precondition failure (ErrConflict), not
// a lost version race: ErrStale is reserved for the runs.version CAS, which is
// the only thing a reload of the run can fix.
func (t *txn) finishStepFrom(ctx context.Context, stepID string, status store.StepStatus, reason string, now time.Time, expected ...store.StepStatus) error {
	return t.finishStepWith(ctx, stepID, status, "{}", reason, now, expected...)
}

// finishStepWith is finishStepFrom with the step's outputs (a JSON object).
func (t *txn) finishStepWith(ctx context.Context, stepID string, status store.StepStatus, outputs, reason string, now time.Time, expected ...store.StepStatus) error {
	_, finished := store.Stamps(nil, nil, false, true, now)
	for _, from := range expected {
		n, err := t.q.UpdateStepInstanceCAS(ctx, gen.UpdateStepInstanceCASParams{
			Status: string(status), Outputs: outputs, Error: reason, FinishedAt: tsOpt(finished),
			ID: stepID, ExpectedStatus: string(from),
		})
		if err != nil {
			return fmt.Errorf("finishing step %q: %w", stepID, err)
		}
		if n > 0 {
			return nil
		}
	}
	return fmt.Errorf("step %q is not in a status that can move to %s: %w", stepID, status, store.ErrConflict)
}

func (t *txn) cancelQueued(ctx context.Context, runID, executionID string, now time.Time) error {
	ex, err := getExecution(ctx, t.q, executionID)
	if err != nil {
		return err
	}
	if ex.RunID != runID {
		return fmt.Errorf("execution %q is not in run %q: %w", executionID, runID, store.ErrNotFound)
	}
	_, finished := store.Stamps(nil, nil, false, true, now)
	n, err := t.q.CancelQueuedExecutionCAS(ctx, gen.CancelQueuedExecutionCASParams{
		FinishedAt: tsOpt(finished), Error: "run cancelled", ID: executionID, RunID: runID,
	})
	if err != nil {
		return fmt.Errorf("canceling execution %q: %w", executionID, err)
	}
	if n == 0 {
		return nil // claimed meanwhile: the owning controller cancels it
	}
	return t.finishStepFrom(ctx, ex.StepInstanceID, store.StepCanceled, "run cancelled", now, store.StepPending, store.StepRunning)
}

// ListActiveRunIDs implements store.RunStore.
func (s *Store) ListActiveRunIDs(ctx context.Context, afterID string, limit int) ([]string, error) {
	ids, err := s.q.ListActiveRunIDs(ctx, gen.ListActiveRunIDsParams{AfterID: afterID, RowLimit: int32(store.ClampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("listing active runs: %w", err)
	}
	return ids, nil
}
