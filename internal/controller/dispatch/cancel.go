package dispatch

import (
	"context"

	"github.com/denyszorinets/damson/internal/controller"
	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

// cancelBatch is the page size of the flagged-attempt query.
const cancelBatch = 500

// cancelOnce sends step.cancel for the dispatched or running attempts of this
// controller that run.advance flagged (ARCHITECTURE 8.3 step 7). Agents without
// a live socket here are skipped: after the agent reconnects the controller
// that adopts it sends the cancel, and if it never returns the lease reaper
// settles the attempt. Each agent is served by its own goroutine so a half-open
// socket delays only itself.
func (d *Dispatcher) cancelOnce(ctx context.Context) {
	rows, err := d.listFlagged(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.log.Error("listing executions to cancel", "error", err)
		}
		return
	}
	current := make(map[string]bool, len(rows))
	byAgent := map[string][]store.Execution{}
	for _, ex := range rows {
		current[ex.AttemptID] = true
		byAgent[ex.AgentID] = append(byAgent[ex.AgentID], ex)
	}
	d.pruneCancelled(current)
	if len(rows) == 0 {
		return
	}
	for _, a := range d.cfg.Agents.Agents() {
		exs := byAgent[a.ID]
		if len(exs) == 0 || a.Peer == nil || !d.begin(cancelKey(a.ID)) {
			continue
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer d.end(cancelKey(a.ID))
			d.cancelAgent(ctx, a, exs)
		}()
	}
}

// cancelKey is the busy-map key of an agent's cancel round. The NUL byte keeps
// it apart from the dispatch key, which is a valid agent id.
func cancelKey(agentID string) string { return "\x00cancel/" + agentID }

// listFlagged reads all flagged attempts of this controller, page by page:
// answered ones stay flagged until the agent reports, so a single page could
// hide the later ones behind them.
func (d *Dispatcher) listFlagged(ctx context.Context) ([]store.Execution, error) {
	var all []store.Execution
	after := ""
	for {
		rctx, cancel := context.WithTimeout(ctx, d.cfg.CallTimeout)
		page, err := d.cfg.Store.ListCancelRequested(rctx, d.cfg.ControllerID, after, cancelBatch)
		cancel()
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < cancelBatch {
			return all, nil
		}
		after = page[len(page)-1].ID
	}
}

// pruneCancelled forgets answered attempts that are no longer flagged.
func (d *Dispatcher) pruneCancelled(current map[string]bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id := range d.cancelled {
		if !current[id] {
			delete(d.cancelled, id)
		}
	}
}

func (d *Dispatcher) cancelAgent(ctx context.Context, a controller.AgentView, exs []store.Execution) {
	for _, ex := range exs {
		d.mu.Lock()
		done := d.cancelled[ex.AttemptID]
		d.mu.Unlock()
		if done {
			continue
		}
		log := d.log.With("run_id", ex.RunID, "execution_id", ex.ID, "attempt_id", ex.AttemptID, "agent_id", a.ID)
		cctx, cancel := context.WithTimeout(ctx, d.cfg.CallTimeout)
		var res protocol.StepCancelResult
		err := a.Peer.Call(cctx, protocol.MethodStepCancel,
			protocol.StepCancel{AttemptID: ex.AttemptID, GraceMS: d.cfg.CancelGrace.Milliseconds()}, &res)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				log.Warn("sending step.cancel failed; retrying on the next poll", "error", err)
			}
			continue
		}
		// known=false changes nothing in the database: the agent may hold a
		// finished result it has not delivered yet, or have lost the attempt;
		// its report or the lease reaper settles it.
		log.Info("step.cancel answered", "known", res.Known)
		d.mu.Lock()
		d.cancelled[ex.AttemptID] = true
		d.mu.Unlock()
	}
}
