package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

var _ store.MetricsStore = (*Store)(nil)

// MetricsSnapshot implements store.MetricsStore.
func (s *Store) MetricsSnapshot(ctx context.Context, seenAfter time.Time) (store.MetricsSnapshot, error) {
	out := store.MetricsSnapshot{RunsByStatus: map[string]int64{}}
	runs, err := s.q.CountRunsByStatus(ctx)
	if err != nil {
		return out, fmt.Errorf("counting runs: %w", err)
	}
	for _, r := range runs {
		out.RunsByStatus[r.Status] = r.N
	}
	execs, err := s.q.CountActiveExecutionsByPool(ctx)
	if err != nil {
		return out, fmt.Errorf("counting executions: %w", err)
	}
	for _, e := range execs {
		out.Executions = append(out.Executions, store.PoolExecutions{Pool: e.Pool, State: e.State, Count: e.N})
	}
	agents, err := s.q.CountConnectedAgentsByPool(ctx, ts(seenAfter))
	if err != nil {
		return out, fmt.Errorf("counting agents: %w", err)
	}
	for _, a := range agents {
		out.Agents = append(out.Agents, store.PoolAgents{Pool: a.Pool, Agents: a.Agents, Slots: a.Slots})
	}
	if out.LogDeletesPending, err = s.q.CountLogObjectDeletes(ctx); err != nil {
		return out, fmt.Errorf("counting log deletes: %w", err)
	}
	return out, nil
}
