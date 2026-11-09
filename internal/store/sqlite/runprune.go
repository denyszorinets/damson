package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

// PruneRuns implements store.Tx.
func (t *txn) PruneRuns(ctx context.Context, before time.Time, limit int, now time.Time) (store.RunPrune, error) {
	var out store.RunPrune
	// A missing actor fails before anything is deleted.
	if _, err := store.AuditFrom(ctx); err != nil {
		return out, err
	}
	runs, err := t.q.ListPrunableRuns(ctx, gen.ListPrunableRunsParams{BeforeAt: sql.NullString{String: fmtTime(before), Valid: true}, RowLimit: int64(store.ClampLimit(limit))})
	if err != nil {
		return out, fmt.Errorf("listing runs to prune: %w", err)
	}
	for _, runID := range runs {
		if err := pruneRun(ctx, t.q, runID, now, &out); err != nil {
			return out, err
		}
	}
	if out.Runs == 0 {
		return out, nil
	}
	ev, err := store.NewRunPruneEvent(ctx, store.RunPruneDetails{RunPrune: out, Before: before}, now)
	if err != nil {
		return out, err
	}
	if err := insertAudit(ctx, t.q, ev); err != nil {
		return out, err
	}
	return out, nil
}

// pruneRun deletes one finished run, its steps, executions and log chunks, and
// queues the deferred delete of external log objects (ADR-0067).
func pruneRun(ctx context.Context, q *gen.Queries, runID string, now time.Time, out *store.RunPrune) error {
	execIDs, err := q.ListExecutionIDsOfRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("listing executions of run %q: %w", runID, err)
	}
	for _, id := range execIDs {
		external, err := q.CountExternalLogChunks(ctx, id)
		if err != nil {
			return fmt.Errorf("counting external log chunks of %q: %w", id, err)
		}
		chunks, err := q.DeleteLogChunksOfExecution(ctx, id)
		if err != nil {
			return fmt.Errorf("deleting log chunks of %q: %w", id, err)
		}
		prefix := store.LogDataPrefix(id)
		l, err := store.PrefixArgs(prefix)
		if err != nil {
			return err
		}
		objects, err := q.DeleteLogDataPrefix(ctx, gen.DeleteLogDataPrefixParams{PrefixLen: int64(l), Prefix: prefix})
		if err != nil {
			return fmt.Errorf("deleting log data of %q: %w", id, err)
		}
		out.Chunks += chunks
		out.Objects += objects
		if external > 0 {
			if err := queueLogObjectDelete(ctx, q, prefix, now, now); err != nil {
				return err
			}
			out.Queued++
		}
	}
	n, err := q.DeleteExecutionsOfRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("deleting executions of run %q: %w", runID, err)
	}
	out.Executions += n
	if _, err := q.DeleteStepInstancesOfRun(ctx, runID); err != nil {
		return fmt.Errorf("deleting steps of run %q: %w", runID, err)
	}
	// The status guard keeps a run that was reopened in the meantime.
	done, err := q.DeleteFinishedRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("deleting run %q: %w", runID, err)
	}
	if done == 0 {
		return fmt.Errorf("run %q changed while it was pruned: %w", runID, store.ErrStale)
	}
	out.Runs++
	return nil
}
