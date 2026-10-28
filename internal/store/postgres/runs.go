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
		ID: r.ID, JobID: r.JobID, JobVersionID: r.JobVersionID, Trigger: json.RawMessage(r.Trigger), Options: json.RawMessage(r.Options),
		Status: store.RunStatus(r.Status), Version: r.Version, CancelRequestedAt: tsPtr(r.CancelRequestedAt),
		ConcurrencyKey: r.ConcurrencyKey, CreatedAt: r.CreatedAt.Time.UTC(),
		StartedAt: tsPtr(r.StartedAt), FinishedAt: tsPtr(r.FinishedAt),
	}
}

func toStep(r gen.StepInstance) store.StepInstance {
	return store.StepInstance{
		ID: r.ID, RunID: r.RunID, Path: r.Path, Kind: r.Kind, Plugin: r.Plugin, Input: json.RawMessage(r.Input),
		Status: store.StepStatus(r.Status), Outputs: json.RawMessage(r.Outputs), Error: r.Error,
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
	if err := checkJobVersion(ctx, q, nr); err != nil {
		return store.RunTree{}, err
	}
	_, err = q.InsertRun(ctx, gen.InsertRunParams{
		ID: id, JobID: nr.JobID, JobVersionID: nr.JobVersionID, Trigger: string(trigger), Options: string(options),
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

// checkJobVersion requires the job and the version to exist and the version
// to belong to the job.
func checkJobVersion(ctx context.Context, q *gen.Queries, nr store.NewRun) error {
	if _, err := q.GetJob(ctx, nr.JobID); errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("job %q: %w", nr.JobID, store.ErrNotFound)
	} else if err != nil {
		return fmt.Errorf("getting job %q: %w", nr.JobID, err)
	}
	jv, err := q.GetJobVersion(ctx, nr.JobVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("job version %q: %w", nr.JobVersionID, store.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("getting job version %q: %w", nr.JobVersionID, err)
	}
	if jv.JobID != nr.JobID {
		return fmt.Errorf("job version %q belongs to another job: %w", nr.JobVersionID, store.ErrConflict)
	}
	return nil
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
			ID: id, RunID: runID, Path: ns.Path, Kind: kind, Plugin: ns.Plugin, Input: string(input),
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
	return getRun(ctx, s.q, id)
}

// getRun reads through q; see getJob.
func getRun(ctx context.Context, q *gen.Queries, id string) (store.Run, error) {
	row, err := q.GetRun(ctx, id)
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
