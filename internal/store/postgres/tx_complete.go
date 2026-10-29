package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

// CompleteExecution implements store.Tx.
func (t *txn) CompleteExecution(ctx context.Context, c store.Completion, now time.Time) (store.Execution, error) {
	if err := store.ValidateCompletion(c); err != nil {
		return store.Execution{}, err
	}
	outputs, err := store.JSONOrEmpty(c.Outputs)
	if err != nil {
		return store.Execution{}, err
	}
	n, err := t.q.CompleteExecutionCAS(ctx, gen.CompleteExecutionCASParams{
		State: string(c.State), Outputs: string(outputs), Error: c.Error, FinishedAt: ts(now), ID: c.ExecutionID, AttemptID: c.AttemptID,
	})
	if err != nil {
		return store.Execution{}, fmt.Errorf("completing execution %q: %w", c.ExecutionID, err)
	}
	if n == 0 {
		return store.Execution{}, fencedMiss(ctx, t.q, c.ExecutionID, "is not running under this attempt")
	}
	ex, err := getExecution(ctx, t.q, c.ExecutionID)
	if err != nil {
		return store.Execution{}, err
	}
	if err := t.settleStep(ctx, ex, now); err != nil {
		return store.Execution{}, err
	}
	return ex, t.touchRuns(ctx, ex.RunID)
}

// ExpireLeases implements store.Tx.
func (t *txn) ExpireLeases(ctx context.Context, now time.Time, limit int) ([]store.Execution, error) {
	rows, err := t.q.SelectExpiredLeases(ctx, gen.SelectExpiredLeasesParams{Now: ts(now), RowLimit: int32(store.ClampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("selecting expired leases: %w", err)
	}
	out := make([]store.Execution, 0, len(rows))
	for _, r := range rows {
		row, err := t.q.MarkExecutionLostCAS(ctx, gen.MarkExecutionLostCASParams{FinishedAt: ts(now), ID: r.ID, AttemptID: r.AttemptID, Now: ts(now)})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("marking execution %q lost: %w", r.ID, err)
		}
		ex := toExecution(row)
		if err := t.settleStep(ctx, ex, now); err != nil {
			return nil, err
		}
		out = append(out, ex)
	}
	// Touch each run once, in id order, after all step rows: a fixed lock
	// order keeps concurrent reapers and run.advance from deadlocking.
	runs := make([]string, len(out))
	for i, ex := range out {
		runs[i] = ex.RunID
	}
	slices.Sort(runs)
	return out, t.touchRuns(ctx, slices.Compact(runs)...)
}

// settleStep moves the step instance of a finished attempt to its terminal
// status. A step that already finished (the run was cancelled meanwhile) is
// left alone.
func (t *txn) settleStep(ctx context.Context, ex store.Execution, now time.Time) error {
	status, reason := store.StepOutcome(ex)
	outputs := "{}"
	if len(ex.Outputs) > 0 {
		outputs = string(ex.Outputs)
	}
	err := t.finishStepWith(ctx, ex.StepInstanceID, status, outputs, reason, now, store.StepPending, store.StepRunning)
	if err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	return nil
}

// touchRuns bumps runs.version so run.advance wakes. A finished run is left alone.
func (t *txn) touchRuns(ctx context.Context, runIDs ...string) error {
	for _, id := range runIDs {
		if _, err := t.TouchRun(ctx, id); err != nil && !errors.Is(err, store.ErrStale) {
			return err
		}
	}
	return nil
}
