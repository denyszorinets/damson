package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

// HostKeyRecorder stores the host key seen on a node, by the run that targeted
// it. It must never overwrite a key (store.NodeStore.ObserveNodeHostKey).
type HostKeyRecorder interface {
	// attemptID and agentID let the store fence the write on an unfinished
	// attempt of that agent.
	ObserveHostKey(ctx context.Context, runID, attemptID, agentID, name, key string) error
}

// HostKeyObserved applies a node.hostkey_observed notification. It is fenced
// like step.complete: the attempt must exist, belong to this agent and be
// unfinished, and the node must be the one its step targets. The key is
// parsed and its fingerprint recomputed here; the stored value is the
// canonical authorized_keys form. Anything that does not hold is dropped
// (a notification has no answer).
func (d *Dispatcher) HostKeyObserved(ctx context.Context, agentID string, m protocol.HostKeyObserved) {
	if d.cfg.HostKeys == nil {
		return
	}
	d.mu.Lock()
	q := d.keys
	d.mu.Unlock()
	if q == nil { // not started (tests): apply inline
		d.applyHostKey(ctx, agentID, m)
		return
	}
	select {
	case q <- hostKeyReport{agentID, m}:
	default:
		d.log.Warn("dropping node.hostkey_observed: queue full", "attempt_id", m.AttemptID, "agent_id", agentID)
	}
}

// hostKeyQueue bounds the reports waiting for the worker.
const hostKeyQueue = 64

type hostKeyReport struct {
	agentID string
	m       protocol.HostKeyObserved
}

func (d *Dispatcher) hostKeyWorker(ctx context.Context, q <-chan hostKeyReport) {
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-q:
			d.applyHostKey(ctx, r.agentID, r.m)
		}
	}
}

func (d *Dispatcher) applyHostKey(ctx context.Context, agentID string, m protocol.HostKeyObserved) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.CallTimeout)
	defer cancel()
	drop := func(reason string, err error) {
		d.log.Info("dropping node.hostkey_observed", "attempt_id", m.AttemptID, "agent_id", agentID, "node", m.Node, "reason", reason, "error", err)
	}
	ex, err := d.cfg.Store.GetExecutionByAttempt(ctx, m.AttemptID)
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && ex.AgentID != agentID):
		drop("unknown attempt", nil)
		return
	case err != nil:
		drop("looking up the attempt", err)
		return
	case ex.State.Terminal():
		drop("attempt finished", nil)
		return
	}
	step, err := d.cfg.Store.GetStepInstance(ctx, ex.StepInstanceID)
	if err != nil {
		drop("loading the step", err)
		return
	}
	res, err := d.cfg.Plugins.Resolve(step.Plugin, pluginreg.Filter{Pool: ex.Pool})
	if err != nil {
		drop("resolving the step's plugin", err)
		return
	}
	var input map[string]any
	if err := json.Unmarshal(step.Input, &input); err != nil {
		drop("decoding the step input", err)
		return
	}
	if names, err := res.Manifest.NodeNames(input); err != nil || !slices.Contains(names, m.Node) {
		drop("the step does not target this node", err)
		return
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(m.HostKey)))
	if err != nil {
		drop("unparsable key", err)
		return
	}
	if ssh.FingerprintSHA256(pub) != m.Fingerprint || pub.Type() != m.KeyType {
		drop("fingerprint or type mismatch", nil)
		return
	}
	key := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if err := d.cfg.HostKeys.ObserveHostKey(ctx, ex.RunID, m.AttemptID, agentID, m.Node, key); err != nil {
		d.log.Warn("recording an observed host key", "run_id", ex.RunID, "execution_id", ex.ID, "attempt_id", m.AttemptID, "node", m.Node, "error", err)
	}
}
