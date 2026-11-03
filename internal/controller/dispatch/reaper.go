package dispatch

import (
	"context"
	"errors"
	"slices"

	"github.com/denyszorinets/damson/internal/store"
)

// renewOnce extends the lease of every attempt the local agents list in their
// heartbeats, in one batched update (ARCHITECTURE 7.3). Attempts the store
// does not renew are stale: the agent learns that from its next
// step.complete acknowledgement.
func (d *Dispatcher) renewOnce(ctx context.Context) {
	var attempts []string
	for _, a := range d.cfg.Agents.Agents() {
		attempts = append(attempts, a.Running...)
	}
	if len(attempts) == 0 {
		return
	}
	slices.Sort(attempts)
	attempts = slices.Compact(attempts)
	cctx, cancel := context.WithTimeout(ctx, d.cfg.CallTimeout)
	defer cancel()
	now := d.cfg.Now()
	if _, err := d.cfg.Store.RenewLeases(cctx, d.cfg.ControllerID, attempts, now.Add(d.cfg.LeaseTTL)); err != nil && ctx.Err() == nil {
		d.log.Error("renewing leases", "attempts", len(attempts), "error", err)
	}
}

// reapOnce marks executions whose lease ended as lost, fails their steps and
// wakes their runs, all in one transaction. Every controller may run it:
// the expiry is a CAS per execution, so overlapping reapers are harmless.
// With a Leader configured only its holder reaps, and each reap transaction is
// fenced by the lease term (ARCHITECTURE 8.4).
func (d *Dispatcher) reapOnce(ctx context.Context) {
	var term int64
	if d.cfg.Leader != nil {
		var ok bool
		if term, ok = d.cfg.Leader.Term(); !ok {
			return
		}
		d.removeDeadControllers(ctx)
	}
	for ctx.Err() == nil {
		var lost []store.Execution
		err := d.cfg.Store.WithTx(ctx, func(tx store.Tx) error {
			now := d.cfg.Now()
			if d.cfg.Leader != nil {
				if err := tx.CheckLeader(ctx, LeaderReaper, d.cfg.ControllerID, term, now); err != nil {
					return err
				}
			}
			var err error
			lost, err = tx.ExpireLeases(ctx, now, reapBatch)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, ex := range lost {
				if seen[ex.RunID] {
					continue
				}
				seen[ex.RunID] = true
				if err := d.cfg.Advance.Enqueue(ctx, tx, ex.RunID); err != nil && !isGone(err) {
					return err
				}
			}
			return nil
		})
		if errors.Is(err, store.ErrStale) && d.cfg.Leader != nil {
			d.log.Warn("reaper lost the leader lease; skipping this round")
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				d.log.Error("reaping expired leases", "error", err)
			}
			return
		}
		for _, ex := range lost {
			d.log.Warn("execution lost: lease expired", "run_id", ex.RunID, "execution_id", ex.ID, "attempt_id", ex.AttemptID, "agent_id", ex.AgentID)
		}
		if len(lost) < reapBatch {
			return
		}
	}
}

// isGone reports an enqueue for a run that finished or vanished meanwhile.
func isGone(err error) bool {
	return errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrStale)
}

// removeDeadControllers deletes the registry rows of controllers that crashed
// without deregistering. Leader only; idempotent.
func (d *Dispatcher) removeDeadControllers(ctx context.Context) {
	n, err := d.cfg.Store.DeleteDeadControllerInstances(ctx, d.cfg.Now().Add(-DeadControllerAfter))
	if err != nil {
		if ctx.Err() == nil {
			d.log.Error("removing dead controller instances", "error", err)
		}
		return
	}
	if n > 0 {
		d.log.Info("removed dead controller instances", "count", n)
	}
}
