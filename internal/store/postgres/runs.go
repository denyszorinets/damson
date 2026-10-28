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

var _ store.RunStore = (*Store)(nil)

func toRun(r gen.Run) store.Run {
	return store.Run{
		ID: r.ID, JobID: r.JobID, JobVersionID: r.JobVersionID, Trigger: r.Trigger, Options: r.Options,
		Status: store.RunStatus(r.Status), Version: r.Version, CancelRequestedAt: tsPtr(r.CancelRequestedAt),
		ConcurrencyKey: r.ConcurrencyKey, CreatedAt: r.CreatedAt.Time.UTC(),
		StartedAt: tsPtr(r.StartedAt), FinishedAt: tsPtr(r.FinishedAt),
	}
}

func toStep(r gen.StepInstance) store.StepInstance {
	return store.StepInstance{
		ID: r.ID, RunID: r.RunID, Path: r.Path, Kind: r.Kind, Plugin: r.Plugin, Input: r.Input,
		Status: store.StepStatus(r.Status), Outputs: r.Outputs, Error: r.Error,
		CreatedAt: r.CreatedAt.Time.UTC(), StartedAt: tsPtr(r.StartedAt), FinishedAt: tsPtr(r.FinishedAt),
	}
}

// CreateRun implements store.RunStore.
func (s *Store) CreateRun(ctx context.Context, nr store.NewRun, now time.Time) (store.RunTree, error) {
	if err := store.ValidateNewRun(nr); err != nil {
		return store.RunTree{}, err
	}
	trigger, err := store.JSONOrEmpty(nr.Trigger)
	if err != nil {
		return store.RunTree{}, err
	}
	options, err := store.JSONOrEmpty(nr.Options)
	if err != nil {
		return store.RunTree{}, err
	}
	id, err := store.NewID()
	if err != nil {
		return store.RunTree{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return store.RunTree{}, fmt.Errorf("beginning run create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	_, err = q.InsertRun(ctx, gen.InsertRunParams{
		ID: id, JobID: nr.JobID, JobVersionID: nr.JobVersionID, Trigger: trigger, Options: options,
		Status: string(store.RunPending), ConcurrencyKey: nr.ConcurrencyKey, CreatedAt: ts(now),
	})
	if hasCode(err, foreignKeyViolation) {
		return store.RunTree{}, fmt.Errorf("job %q or job version %q: %w", nr.JobID, nr.JobVersionID, store.ErrNotFound)
	}
	if err != nil {
		return store.RunTree{}, fmt.Errorf("creating run: %w", err)
	}
	steps, err := insertSteps(ctx, q, id, nr.Steps, now)
	if err != nil {
		return store.RunTree{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.RunTree{}, fmt.Errorf("committing run create: %w", err)
	}
	run := store.Run{
		ID: id, JobID: nr.JobID, JobVersionID: nr.JobVersionID, Trigger: trigger, Options: options,
		Status: store.RunPending, Version: 1, ConcurrencyKey: nr.ConcurrencyKey, CreatedAt: store.NormalizeTime(now),
	}
	return store.RunTree{Run: run, Steps: steps}, nil
}

func insertSteps(ctx context.Context, q *gen.Queries, runID string, steps []store.NewStepInstance, now time.Time) ([]store.StepNode, error) {
	out := make([]store.StepNode, 0, len(steps))
	for _, ns := range steps {
		id, err := store.NewID()
		if err != nil {
			return nil, err
		}
		input, err := store.JSONOrEmpty(ns.Input)
		if err != nil {
			return nil, err
		}
		kind := store.KindOrDefault(ns.Kind)
		_, err = q.InsertStepInstance(ctx, gen.InsertStepInstanceParams{
			ID: id, RunID: runID, Path: ns.Path, Kind: kind, Plugin: ns.Plugin, Input: input,
			Status: string(store.StepPending), CreatedAt: ts(now),
		})
		if hasCode(err, uniqueViolation) {
			return nil, fmt.Errorf("step path %q: %w", ns.Path, store.ErrConflict)
		}
		if err != nil {
			return nil, fmt.Errorf("creating step %q: %w", ns.Path, err)
		}
		node := store.StepNode{StepInstance: store.StepInstance{
			ID: id, RunID: runID, Path: ns.Path, Kind: kind, Plugin: ns.Plugin, Input: input,
			Status: store.StepPending, Outputs: []byte("{}"), CreatedAt: store.NormalizeTime(now),
		}}
		if ns.Execution != nil {
			ex, err := insertExecution(ctx, q, runID, id, 1, *ns.Execution, now)
			if err != nil {
				return nil, err
			}
			node.Executions = []store.Execution{ex}
		}
		out = append(out, node)
	}
	return out, nil
}

func insertExecution(ctx context.Context, q *gen.Queries, runID, stepID string, attemptNo int, ne store.NewExecution, now time.Time) (store.Execution, error) {
	id, err := store.NewID()
	if err != nil {
		return store.Execution{}, err
	}
	attemptID, err := store.NewID()
	if err != nil {
		return store.Execution{}, err
	}
	_, err = q.InsertExecution(ctx, gen.InsertExecutionParams{
		ID: id, RunID: runID, StepInstanceID: stepID, AttemptNo: int32(attemptNo), AttemptID: attemptID,
		ScheduledAt: ts(ne.ScheduledAt), Priority: int32(ne.Priority), Pool: ne.Pool, CreatedAt: ts(now),
	})
	if hasCode(err, uniqueViolation) {
		return store.Execution{}, fmt.Errorf("step %q attempt %d: %w", stepID, attemptNo, store.ErrConflict)
	}
	if err != nil {
		return store.Execution{}, fmt.Errorf("creating execution for step %q: %w", stepID, err)
	}
	return store.Execution{
		ID: id, RunID: runID, StepInstanceID: stepID, AttemptNo: attemptNo, AttemptID: attemptID,
		State: store.ExecQueued, ScheduledAt: store.NormalizeTime(ne.ScheduledAt), Priority: ne.Priority, Pool: ne.Pool,
		Outputs: []byte("{}"), CreatedAt: store.NormalizeTime(now),
	}, nil
}

// GetRun implements store.RunStore.
func (s *Store) GetRun(ctx context.Context, id string) (store.Run, error) {
	row, err := s.q.GetRun(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Run{}, fmt.Errorf("run %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Run{}, fmt.Errorf("getting run %q: %w", id, err)
	}
	return toRun(row), nil
}

// GetRunTree implements store.RunStore.
func (s *Store) GetRunTree(ctx context.Context, id string) (store.RunTree, error) {
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return store.RunTree{}, err
	}
	steps, err := s.q.ListStepInstancesByRun(ctx, id)
	if err != nil {
		return store.RunTree{}, fmt.Errorf("listing steps of run %q: %w", id, err)
	}
	execs, err := s.q.ListExecutionsByRun(ctx, id)
	if err != nil {
		return store.RunTree{}, fmt.Errorf("listing executions of run %q: %w", id, err)
	}
	tree := store.RunTree{Run: run, Steps: make([]store.StepNode, 0, len(steps))}
	idx := make(map[string]int, len(steps))
	for _, st := range steps {
		idx[st.ID] = len(tree.Steps)
		tree.Steps = append(tree.Steps, store.StepNode{StepInstance: toStep(st)})
	}
	for _, e := range execs {
		i, ok := idx[e.StepInstanceID]
		if !ok {
			continue
		}
		tree.Steps[i].Executions = append(tree.Steps[i].Executions, toExecution(e))
	}
	return tree, nil
}

// GetStepInstance implements store.RunStore.
func (s *Store) GetStepInstance(ctx context.Context, id string) (store.StepInstance, error) {
	row, err := s.q.GetStepInstance(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.StepInstance{}, fmt.Errorf("step instance %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.StepInstance{}, fmt.Errorf("getting step instance %q: %w", id, err)
	}
	return toStep(row), nil
}

// ListRuns implements store.RunStore.
func (s *Store) ListRuns(ctx context.Context, jobID, cursor string, limit int) (store.RunPage, error) {
	limit = store.ClampLimit(limit)
	rows, err := s.q.ListRunsBefore(ctx, gen.ListRunsBeforeParams{JobID: jobID, BeforeID: cursor, RowLimit: int32(limit) + 1})
	if err != nil {
		return store.RunPage{}, fmt.Errorf("listing runs: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	page := store.RunPage{Runs: make([]store.Run, 0, len(rows))}
	for _, r := range rows {
		page.Runs = append(page.Runs, toRun(r))
	}
	if more {
		page.NextCursor = page.Runs[len(page.Runs)-1].ID
	}
	return page, nil
}

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
	last, err := q.LatestAttemptNo(ctx, stepInstanceID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return store.Execution{}, fmt.Errorf("reading latest attempt of step %q: %w", stepInstanceID, err)
	}
	ex, err := insertExecution(ctx, q, runID, stepInstanceID, int(last)+1, ne, now)
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
		Status: string(u.Status), Outputs: outputs, Error: u.Error, StartedAt: tsOpt(started), FinishedAt: tsOpt(finished),
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
