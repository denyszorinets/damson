package advance

import (
	"context"
	"errors"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// sweepBatch is how many runs one sweep transaction enqueues.
const sweepBatch = 100

// sweepLoop runs Sweep every a.sweep until ctx ends.
func (a *Advancer) sweepLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(a.sweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.Sweep(ctx); err != nil && ctx.Err() == nil {
				a.log.Error("run.advance sweep failed", "error", err)
			}
		}
	}
}

// Sweep is the safety net behind transactional enqueue: it enqueues
// run.advance for every pending or running run. It covers runs created before
// the queue existed and any window in which a job was lost despite the
// transaction (an operator purging river_job, a bug). Uniqueness by
// (run, version) collapses it into the jobs already waiting; a run with
// nothing to do costs one no-op job. Every controller may sweep: the
// duplicates are harmless, so no leader lease is needed (ARCHITECTURE 8.4).
func (a *Advancer) Sweep(ctx context.Context) error {
	after := ""
	for {
		ids, err := a.st.ListActiveRunIDs(ctx, after, sweepBatch)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		err = a.st.WithTx(ctx, func(tx store.Tx) error {
			for _, id := range ids {
				err := a.Enqueue(ctx, tx, id)
				if errors.Is(err, store.ErrNotFound) {
					continue // deleted since it was listed
				}
				if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		after = ids[len(ids)-1]
	}
}
