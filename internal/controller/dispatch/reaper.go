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
// the expiry is a CAS per execution, so overlapping reapers are harmless
// (ARCHITECTURE 8.4; the leader lease arrives with M3).
func (d *Dispatcher) reapOnce(ctx context.Context) {
	for ctx.Err() == nil {
		var lost []store.Execution
		err := d.cfg.Store.WithTx(ctx, func(tx store.Tx) error {
			var err error
			lost, err = tx.ExpireLeases(ctx, d.cfg.Now(), reapBatch)
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
