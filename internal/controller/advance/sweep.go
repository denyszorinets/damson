package advance

import (
	"context"
	"errors"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// sweepBatch is how many runs one sweep transaction enqueues.
const sweepBatch = 100

// sweepLoop runs Sweep at start, so a restarted controller does not wait a
// whole interval to pick up what a crash stranded, and then every a.sweep
// until ctx ends.
func (a *Advancer) sweepLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(a.sweep)
	defer t.Stop()
	for {
		if err := a.Sweep(ctx); err != nil && ctx.Err() == nil {
			a.log.Error("run.advance sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sweep is the safety net behind transactional enqueue: it enqueues
// run.advance for every pending or running run. It covers runs created before
// the queue existed and any window in which a job was lost despite the
// transaction (an operator purging river_job, a bug) and a controller that
// crashed mid-advance. Its jobs carry the sweep epoch, the current time cut
// to a multiple of the interval: sweeps of one interval (on any controller)
// collapse into each other, but never into an event job, so a job a dead
// controller left "running" cannot swallow the sweep. A run with nothing to
// do costs one no-op job. Every controller may sweep: the
// duplicates are harmless, so no leader lease is needed (ARCHITECTURE 8.4).
func (a *Advancer) Sweep(ctx context.Context) error {
	// With sweeping disabled (a hand-made call) there is no interval to cut
	// to: epoch 0 is the key of an event job, so repeated calls collapse.
	var epoch int64
	if a.sweep > 0 {
		epoch = a.core.now().Truncate(a.sweep).UnixMilli()
	}
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
				err := a.enqueue(ctx, tx, id, epoch)
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
