package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

// Register installs the step.complete handler on one agent socket. It
// implements controller.SessionHook.
func (d *Dispatcher) Register(peer *protocol.Peer, agentID string) {
	peer.Handle(protocol.MethodStepComplete, func(ctx context.Context, p protocol.Params) (any, error) {
		var m protocol.StepComplete
		if err := p.Decode(&m); err != nil {
			return nil, err
		}
		return d.Complete(ctx, agentID, m)
	})
}

// Complete applies an agent's terminal report. The attempt_id fences it: a
// report for an attempt that is not current, not owned by this agent or
// already finished is acknowledged as stale and changes nothing. The
// execution, its step, the run version and the run.advance job commit
// together; the agent is acknowledged after the commit.
func (d *Dispatcher) Complete(ctx context.Context, agentID string, m protocol.StepComplete) (protocol.StepCompleteAck, error) {
	stale := protocol.StepCompleteAck{Acknowledged: true, Stale: true}
	c, err := completion(m)
	if err != nil {
		return protocol.StepCompleteAck{}, err
	}
	// Finish what the agent reported even if the socket drops now, but bounded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.CallTimeout)
	defer cancel()
	ex, err := d.cfg.Store.GetExecutionByAttempt(ctx, m.AttemptID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && ex.AgentID != agentID) {
		d.log.Info("discarding step.complete for an unknown attempt", "attempt_id", m.AttemptID, "agent_id", agentID)
		return stale, nil
	}
	if err != nil {
		return protocol.StepCompleteAck{}, fmt.Errorf("looking up attempt: %w", err)
	}
	c.ExecutionID = ex.ID
	err = d.cfg.Store.WithTx(ctx, func(tx store.Tx) error {
		if _, err := tx.CompleteExecution(ctx, c, d.cfg.Now()); err != nil {
			return err
		}
		return d.cfg.Advance.Enqueue(ctx, tx, ex.RunID)
	})
	switch {
	case errors.Is(err, store.ErrStale):
		d.log.Info("discarding stale step.complete", "run_id", ex.RunID, "execution_id", ex.ID, "attempt_id", m.AttemptID, "agent_id", agentID)
		return stale, nil
	case err != nil:
		return protocol.StepCompleteAck{}, fmt.Errorf("completing execution: %w", err)
	}
	return protocol.StepCompleteAck{Acknowledged: true}, nil
}

func completion(m protocol.StepComplete) (store.Completion, error) {
	c := store.Completion{AttemptID: m.AttemptID, State: store.ExecutionState(m.Status)}
	if !c.State.Reportable() {
		return c, fmt.Errorf("step.complete status %q is not a terminal status", m.Status)
	}
	if m.Outputs != nil {
		raw, err := json.Marshal(m.Outputs)
		if err != nil {
			return c, fmt.Errorf("encoding outputs: %w", err)
		}
		c.Outputs = raw
	}
	if m.Error != nil {
		c.Error = m.Error.Message
	}
	return c, nil
}
