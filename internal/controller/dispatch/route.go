package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/denyszorinets/damson/internal/controller"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
)

// routeByNode handles a claimed execution whose pool is store.PoolByNode: it
// finds the pool of the node the step targets and, when that is not the
// claiming agent's pool, queues the execution again in that pool. It reports
// whether the execution was dealt with (routed, failed or requeued); false
// means the claiming agent is in the right pool and the normal send goes on.
// No secret is read here, so an agent in the wrong pool never gets a bundle
// (ADR-0052).
func (d *Dispatcher) routeByNode(ctx context.Context, a controller.AgentView, ex store.Execution, log *slog.Logger) bool {
	pool, err := d.targetPool(ctx, ex)
	var perm *permanent
	if errors.As(err, &perm) {
		log.Warn("execution cannot be routed: failing it", "error", err)
		d.failDispatched(ctx, a, ex, err)
		return true
	}
	if err != nil {
		log.Warn("execution cannot be routed yet", "error", err)
		d.requeue(ctx, a, ex, d.cfg.Now().Add(d.cfg.IncapableRetry), log)
		return true
	}
	if pool == a.Pool {
		return false
	}
	d.cfg.Agents.Release(a.ID, 1)
	if d.cfg.Invalidate != nil {
		d.cfg.Invalidate(ex.AttemptID)
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.CallTimeout)
	defer cancel()
	err = d.cfg.Store.RouteExecution(rctx, ex.ID, ex.AttemptID, pool, d.cfg.Now())
	switch {
	case errors.Is(err, store.ErrStale):
		d.cancelUndelivered(rctx, ex, log)
	case err != nil:
		log.Error("routing execution", "pool", pool, "error", err)
	default:
		log.Info("execution routed to the pool of its node", "pool", pool)
	}
	return true
}

// targetPool is the pool a step must run in: the one pool of the nodes it
// names, else the default pool. Nodes without a pool add no constraint.
func (d *Dispatcher) targetPool(ctx context.Context, ex store.Execution) (string, error) {
	step, err := d.cfg.Store.GetStepInstance(ctx, ex.StepInstanceID)
	if err != nil {
		return "", fmt.Errorf("loading step instance: %w", err)
	}
	res, err := d.cfg.Plugins.Resolve(step.Plugin, pluginreg.Filter{})
	if err != nil {
		return "", fmt.Errorf("resolving plugin %q: %w", step.Plugin, err)
	}
	ps, ok := d.cfg.Nodes.(PoolSource)
	if !ok {
		return store.DefaultPool, nil
	}
	var input map[string]any
	if len(step.Input) > 0 {
		if err := json.Unmarshal(step.Input, &input); err != nil {
			return "", fmt.Errorf("decoding step input: %w", err)
		}
	}
	names, err := res.Manifest.NodeNames(input)
	if err != nil {
		return "", &permanent{err}
	}
	var pools []string
	for _, name := range names {
		p, err := ps.NodePool(ctx, ex.RunID, name)
		if err != nil {
			return "", nodeError(name, err)
		}
		if p != "" && !slices.Contains(pools, p) {
			pools = append(pools, p)
		}
	}
	switch len(pools) {
	case 0:
		return store.DefaultPool, nil
	case 1:
		return pools[0], nil
	}
	sort.Strings(pools)
	return "", &permanent{fmt.Errorf("step targets nodes in pools %s; set runs_on.pool", strings.Join(pools, ", "))}
}
