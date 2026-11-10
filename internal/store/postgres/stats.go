package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

var _ store.StatsStore = (*Store)(nil)

// ProjectCounts implements store.StatsStore.
func (s *Store) ProjectCounts(ctx context.Context, failedSince time.Time) (map[string]store.ProjectCounts, error) {
	runs, err := s.q.CountRunsByProject(ctx, ts(failedSince))
	if err != nil {
		return nil, fmt.Errorf("counting runs by project: %w", err)
	}
	jobs, err := s.q.CountJobsByProject(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting jobs by project: %w", err)
	}
	out := map[string]store.ProjectCounts{}
	for _, j := range jobs {
		c := out[j.ProjectID]
		c.Jobs = int(j.N)
		out[j.ProjectID] = c
	}
	for _, r := range runs {
		c := out[r.ProjectID]
		switch r.Status {
		case string(store.RunFailed):
			c.Failed24h = int(r.N)
		case string(store.RunRunning):
			c.Running = int(r.N)
		}
		out[r.ProjectID] = c
	}
	return out, nil
}
