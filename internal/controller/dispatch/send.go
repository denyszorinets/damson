package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/denyszorinets/damson/internal/controller"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

// retryIncapable delays an execution claimed by an agent that cannot run it,
// so a step nobody can run does not spin the claim loop.
const retryIncapable = 5 * time.Second

// dispatchOnce starts a dispatch round for every connected agent with free
// slots and does not wait for it: a half-open socket must not delay the other
// agents. At most one round per agent is in flight; Stop waits for them.
func (d *Dispatcher) dispatchOnce(ctx context.Context) {
	for _, a := range d.cfg.Agents.Agents() {
		if a.Free <= 0 || a.Peer == nil || !d.begin(a.ID) {
			continue
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer d.end(a.ID)
			d.dispatchAgent(ctx, a)
		}()
	}
}

func (d *Dispatcher) begin(agentID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.busy[agentID] {
		return false
	}
	d.busy[agentID] = true
	return true
}

func (d *Dispatcher) end(agentID string) {
	d.mu.Lock()
	delete(d.busy, agentID)
	d.mu.Unlock()
}

func (d *Dispatcher) dispatchAgent(ctx context.Context, a controller.AgentView) {
	n := d.cfg.Agents.Reserve(a.ID, a.Free)
	if n == 0 {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, d.cfg.CallTimeout)
	execs, err := d.cfg.Store.ClaimExecutions(cctx, store.ClaimParams{
		AgentID: a.ID, ControllerID: d.cfg.ControllerID, Pools: []string{a.Pool}, Limit: n, LeaseTTL: d.cfg.LeaseTTL,
	}, d.cfg.Now())
	cancel()
	if err != nil {
		d.cfg.Agents.Release(a.ID, n)
		if ctx.Err() == nil {
			d.log.Error("claiming executions", "agent_id", a.ID, "error", err)
		}
		return
	}
	d.cfg.Agents.Release(a.ID, n-len(execs))
	for i, ex := range execs {
		if !d.send(ctx, a, ex) {
			// The socket is not usable: the rest of the batch was never sent,
			// so put it back now rather than let its leases run out.
			for _, rest := range execs[i+1:] {
				d.requeue(ctx, a, rest, d.cfg.Now(), d.log)
			}
			return
		}
	}
}

// send delivers one claimed execution and reports whether the socket is still
// usable for the rest of the batch.
//
// Delivery semantics (at most once): a failure that leaves it unknown whether
// the agent started the step (timeout, closed socket) does not requeue the
// execution, because a second agent could then run it too. The execution is
// cancelled on the agent first; it is requeued only if that is answered (the
// agent also tombstones the attempt, so a late step.execute is rejected).
// Otherwise it stays dispatched and the lease reaper settles it.
func (d *Dispatcher) send(ctx context.Context, a controller.AgentView, ex store.Execution) bool {
	log := d.log.With("run_id", ex.RunID, "execution_id", ex.ID, "attempt_id", ex.AttemptID, "agent_id", a.ID)
	req, err := d.buildRequest(ctx, a, ex)
	if err != nil {
		log.Warn("execution cannot be sent to this agent", "error", err)
		d.requeue(ctx, a, ex, d.cfg.Now().Add(retryIncapable), log)
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, d.cfg.CallTimeout)
	defer cancel()
	var res protocol.StepExecuteResult
	if err := a.Peer.Call(cctx, protocol.MethodStepExecute, req, &res); err != nil {
		log.Warn("step.execute outcome unknown", "error", err)
		d.cancelAndRequeue(ctx, a, ex, log)
		return false
	}
	if !res.Accepted {
		log.Info("agent rejected step.execute", "reason", res.Reason)
		d.requeue(ctx, a, ex, d.cfg.Now().Add(time.Second), log)
		return true
	}
	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.CallTimeout)
	defer scancel()
	err = d.cfg.Store.MarkExecutionRunning(sctx, ex.ID, ex.AttemptID, d.cfg.Now())
	if err != nil && !errors.Is(err, store.ErrStale) {
		log.Error("marking execution running", "error", err) // the lease reaper settles it if this sticks
	}
	return true
}

func (d *Dispatcher) cancelAndRequeue(ctx context.Context, a controller.AgentView, ex store.Execution, log *slog.Logger) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.CallTimeout)
	defer cancel()
	var res protocol.StepCancelResult
	err := a.Peer.Call(cctx, protocol.MethodStepCancel, protocol.StepCancel{AttemptID: ex.AttemptID}, &res)
	if err != nil {
		log.Warn("could not cancel the attempt; leaving it to the lease reaper", "error", err)
		return
	}
	d.requeue(ctx, a, ex, d.cfg.Now(), log)
}

func (d *Dispatcher) buildRequest(ctx context.Context, a controller.AgentView, ex store.Execution) (protocol.StepExecute, error) {
	step, err := d.cfg.Store.GetStepInstance(ctx, ex.StepInstanceID)
	if err != nil {
		return protocol.StepExecute{}, fmt.Errorf("loading step instance: %w", err)
	}
	res, err := d.cfg.Plugins.Resolve(step.Plugin, pluginreg.Filter{Pool: a.Pool})
	if err != nil {
		return protocol.StepExecute{}, fmt.Errorf("resolving plugin %q: %w", step.Plugin, err)
	}
	if !slices.ContainsFunc(res.Agents, func(p pluginreg.Provider) bool { return p.AgentID == a.ID }) {
		return protocol.StepExecute{}, fmt.Errorf("agent cannot run plugin %q", step.Plugin)
	}
	var input map[string]any
	if len(step.Input) > 0 {
		if err := json.Unmarshal(step.Input, &input); err != nil {
			return protocol.StepExecute{}, fmt.Errorf("decoding step input: %w", err)
		}
	}
	return protocol.StepExecute{
		AttemptID: ex.AttemptID, ExecutionID: ex.ID, StepPath: step.Path,
		PluginName: res.Manifest.Name, PluginVersion: res.Manifest.Version, Input: input,
	}, nil
}

// requeue puts a dispatched execution back in the queue (fenced) and returns
// its slot.
func (d *Dispatcher) requeue(ctx context.Context, a controller.AgentView, ex store.Execution, at time.Time, log *slog.Logger) {
	d.cfg.Agents.Release(a.ID, 1)
	if d.cfg.Invalidate != nil {
		d.cfg.Invalidate(ex.AttemptID)
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.CallTimeout)
	defer cancel()
	err := d.cfg.Store.RequeueExecution(rctx, ex.ID, ex.AttemptID, at)
	if err != nil && !errors.Is(err, store.ErrStale) {
		log.Error("requeueing execution", "error", err)
	}
}
