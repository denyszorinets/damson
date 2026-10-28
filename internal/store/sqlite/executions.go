package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

var _ store.ExecutionStore = (*Store)(nil)

func toExecution(r gen.Execution) (store.Execution, error) {
	out := store.Execution{
		ID: r.ID, RunID: r.RunID, StepInstanceID: r.StepInstanceID, AttemptNo: int(r.AttemptNo),
		AttemptID: r.AttemptID, State: store.ExecutionState(r.State), Priority: int(r.Priority), Pool: r.Pool,
		AgentID: r.AgentID.String, ControllerID: r.ControllerID.String, Outputs: json.RawMessage(r.Outputs), Error: r.Error,
	}
	var err error
	if out.ScheduledAt, err = parseTime(r.ScheduledAt); err != nil {
		return store.Execution{}, err
	}
	if out.CreatedAt, err = parseTime(r.CreatedAt); err != nil {
		return store.Execution{}, err
	}
	if out.LeaseExpiresAt, err = parseNullTime(r.LeaseExpiresAt); err != nil {
		return store.Execution{}, err
	}
	if out.StartedAt, err = parseNullTime(r.StartedAt); err != nil {
		return store.Execution{}, err
	}
	if out.FinishedAt, err = parseNullTime(r.FinishedAt); err != nil {
		return store.Execution{}, err
	}
	return out, nil
}

// GetExecution implements store.ExecutionStore.
func (s *Store) GetExecution(ctx context.Context, id string) (store.Execution, error) {
	return getExecution(ctx, s.q, id)
}

func getExecution(ctx context.Context, q *gen.Queries, id string) (store.Execution, error) {
	row, err := q.GetExecution(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Execution{}, fmt.Errorf("execution %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Execution{}, fmt.Errorf("getting execution %q: %w", id, err)
	}
	return toExecution(row)
}

// GetExecutionByAttempt implements store.ExecutionStore.
func (s *Store) GetExecutionByAttempt(ctx context.Context, attemptID string) (store.Execution, error) {
	row, err := s.q.GetExecutionByAttempt(ctx, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Execution{}, fmt.Errorf("attempt %q: %w", attemptID, store.ErrNotFound)
	}
	if err != nil {
		return store.Execution{}, fmt.Errorf("getting attempt %q: %w", attemptID, err)
	}
	return toExecution(row)
}

// ClaimExecutions implements store.ExecutionStore. The connection opens
// write transactions as IMMEDIATE, so concurrent claimers serialize and the
// select-then-update below cannot hand one execution to two callers.
func (s *Store) ClaimExecutions(ctx context.Context, p store.ClaimParams, now time.Time) ([]store.Execution, error) {
	if err := store.ValidateClaim(p); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	ids, err := q.SelectClaimableExecutions(ctx, gen.SelectClaimableExecutionsParams{ScheduledAt: fmtTime(now), Pools: p.Pools, Limit: int64(store.ClampLimit(p.Limit))})
	if err != nil {
		return nil, fmt.Errorf("selecting claimable executions: %w", err)
	}
	out := make([]store.Execution, 0, len(ids))
	for _, id := range ids {
		attemptID, err := store.NewID()
		if err != nil {
			return nil, err
		}
		row, err := q.ClaimExecutionCAS(ctx, gen.ClaimExecutionCASParams{
			AttemptID: attemptID, AgentID: nullStr(p.AgentID), ControllerID: nullStr(p.ControllerID),
			LeaseExpiresAt: someTime(now.Add(p.LeaseTTL)), ID: id,
		})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("claiming execution %q: %w", id, err)
		}
		ex, err := toExecution(row)
		if err != nil {
			return nil, err
		}
		if err := dropPreviousAttemptLogs(ctx, q, id); err != nil {
			return nil, err
		}
		out = append(out, ex)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing claim: %w", err)
	}
	return out, nil
}

// dropPreviousAttemptLogs removes the log chunks of earlier attempts in the
// claim's transaction: the attempt id rotates here, after which their writers
// are fenced out, so nothing can race the delete.
func dropPreviousAttemptLogs(ctx context.Context, q *gen.Queries, executionID string) error {
	if _, err := q.DeleteLogChunksOfExecution(ctx, executionID); err != nil {
		return fmt.Errorf("dropping previous log chunks of %q: %w", executionID, err)
	}
	prefix := store.LogDataPrefix(executionID)
	l, err := store.PrefixArgs(prefix)
	if err != nil {
		return err
	}
	if _, err := q.DeleteLogDataPrefix(ctx, gen.DeleteLogDataPrefixParams{PrefixLen: int64(l), Prefix: prefix}); err != nil {
		return fmt.Errorf("dropping previous log data of %q: %w", executionID, err)
	}
	return nil
}

// fencedMiss classifies a fenced CAS that changed no row: the execution is
// absent (ErrNotFound) or the attempt/state moved on (ErrStale).
func fencedMiss(ctx context.Context, q *gen.Queries, executionID, what string) error {
	if _, err := getExecution(ctx, q, executionID); err != nil {
		return err
	}
	return fmt.Errorf("execution %q %s: %w", executionID, what, store.ErrStale)
}

// MarkExecutionRunning implements store.ExecutionStore.
func (s *Store) MarkExecutionRunning(ctx context.Context, executionID, attemptID string, now time.Time) error {
	n, err := s.q.MarkExecutionRunningCAS(ctx, gen.MarkExecutionRunningCASParams{StartedAt: someTime(now), ID: executionID, AttemptID: attemptID})
	if err != nil {
		return fmt.Errorf("marking execution %q running: %w", executionID, err)
	}
	if n == 0 {
		return fencedMiss(ctx, s.q, executionID, "is not dispatched under this attempt")
	}
	return nil
}

// renewChunk bounds the IN list well under SQLite's variable limit.
const renewChunk = 500

// RenewLeases implements store.ExecutionStore.
func (s *Store) RenewLeases(ctx context.Context, controllerID string, attemptIDs []string, leaseExpiresAt time.Time) ([]string, error) {
	if len(attemptIDs) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning lease renewal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	renewed := make([]string, 0, len(attemptIDs))
	for start := 0; start < len(attemptIDs); start += renewChunk {
		end := min(start+renewChunk, len(attemptIDs))
		got, err := q.RenewLeases(ctx, gen.RenewLeasesParams{
			MAX: fmtTime(leaseExpiresAt), AttemptIds: attemptIDs[start:end], ControllerID: nullStr(controllerID),
		})
		if err != nil {
			return nil, fmt.Errorf("renewing leases: %w", err)
		}
		renewed = append(renewed, got...)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing lease renewal: %w", err)
	}
	return renewed, nil
}

// ExpireLeases implements store.ExecutionStore.
func (s *Store) ExpireLeases(ctx context.Context, now time.Time, limit int) ([]store.Execution, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning lease expiry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	rows, err := q.SelectExpiredLeases(ctx, gen.SelectExpiredLeasesParams{Now: someTime(now), RowLimit: int64(store.ClampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("selecting expired leases: %w", err)
	}
	out := make([]store.Execution, 0, len(rows))
	for _, r := range rows {
		row, err := q.MarkExecutionLostCAS(ctx, gen.MarkExecutionLostCASParams{FinishedAt: someTime(now), ID: r.ID, AttemptID: r.AttemptID, Now: someTime(now)})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("marking execution %q lost: %w", r.ID, err)
		}
		ex, err := toExecution(row)
		if err != nil {
			return nil, err
		}
		out = append(out, ex)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing lease expiry: %w", err)
	}
	return out, nil
}

// CompleteExecution implements store.ExecutionStore.
func (s *Store) CompleteExecution(ctx context.Context, c store.Completion, now time.Time) (store.Execution, error) {
	if err := store.ValidateCompletion(c); err != nil {
		return store.Execution{}, err
	}
	outputs, err := store.JSONOrEmpty(c.Outputs)
	if err != nil {
		return store.Execution{}, err
	}
	n, err := s.q.CompleteExecutionCAS(ctx, gen.CompleteExecutionCASParams{
		State: string(c.State), Outputs: string(outputs), Error: c.Error, FinishedAt: someTime(now), ID: c.ExecutionID, AttemptID: c.AttemptID,
	})
	if err != nil {
		return store.Execution{}, fmt.Errorf("completing execution %q: %w", c.ExecutionID, err)
	}
	if n == 0 {
		return store.Execution{}, fencedMiss(ctx, s.q, c.ExecutionID, "is not running under this attempt")
	}
	return s.GetExecution(ctx, c.ExecutionID)
}

// RequeueExecution implements store.ExecutionStore.
func (s *Store) RequeueExecution(ctx context.Context, executionID, attemptID string, scheduledAt time.Time) error {
	n, err := s.q.RequeueExecutionCAS(ctx, gen.RequeueExecutionCASParams{ScheduledAt: fmtTime(scheduledAt), ID: executionID, AttemptID: attemptID})
	if err != nil {
		return fmt.Errorf("requeueing execution %q: %w", executionID, err)
	}
	if n == 0 {
		return fencedMiss(ctx, s.q, executionID, "is not dispatched under this attempt")
	}
	return nil
}
