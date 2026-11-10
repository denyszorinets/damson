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

// JobRunStats implements store.StatsStore.
func (s *Store) JobRunStats(ctx context.Context, projectID string) (map[string]store.JobRunStats, error) {
	rows, err := s.q.LatestRunsByJobAndStatus(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("reading the latest runs of project %s: %w", projectID, err)
	}
	runs := make([]store.LatestRun, 0, len(rows))
	for _, r := range rows {
		runs = append(runs, store.LatestRun{
			JobID: r.JobID, ID: r.ID, Status: r.Status, CreatedAt: r.CreatedAt.Time.UTC(),
			StartedAt: tsPtr(r.StartedAt), FinishedAt: tsPtr(r.FinishedAt),
		})
	}
	return store.FoldLatestRuns(runs), nil
}
