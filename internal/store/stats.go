package store

import (
	"context"
	"time"
)

// ProjectCounts are the counts shown on a project of the tree. They cover the
// project's own jobs and runs only; the API adds up the visible subtree.
type ProjectCounts struct {
	Jobs      int
	Failed24h int // runs that failed since the cutoff
	Running   int
}

// StatsStore counts jobs and runs per project with two grouped queries, never
// one per project.
type StatsStore interface {
	// ProjectCounts returns the counts by project id; projects with none are
	// absent. failedSince is the cutoff for failed runs; archived jobs and their
	// runs are not counted.
	ProjectCounts(ctx context.Context, failedSince time.Time) (map[string]ProjectCounts, error)

	// JobRunStats returns, by job id, how each job of the project last ran. A
	// job that never ran, and an archived job, are absent.
	JobRunStats(ctx context.Context, projectID string) (map[string]JobRunStats, error)
}

// LatestRun is the newest run of one status of one job: the row the backends
// read, before FoldLatestRuns turns the rows of a project into JobRunStats.
type LatestRun struct {
	JobID, ID, Status string
	CreatedAt         time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
}

// JobRunStats is how a job last ran, read from its newest run of each status.
type JobRunStats struct {
	LastStatus    RunStatus  // status of the newest run of any status
	LastRunAt     time.Time  // when that run finished, else started, else was created
	LastSuccessAt *time.Time // when the newest succeeded run finished
	LastFailureAt *time.Time // when the newest failed run finished
	// LastDuration is how long the newest finished run took, from its start; nil
	// when no finished run recorded a start.
	LastDuration *time.Duration
}

func (r LatestRun) at() time.Time {
	switch {
	case r.FinishedAt != nil:
		return *r.FinishedAt
	case r.StartedAt != nil:
		return *r.StartedAt
	}
	return r.CreatedAt
}

// FoldLatestRuns turns the newest run per job and status into one JobRunStats
// per job. Run ids are time ordered, so the larger id is the newer run.
func FoldLatestRuns(rows []LatestRun) map[string]JobRunStats {
	newest := map[string]LatestRun{}
	finished := map[string]LatestRun{} // newest succeeded, failed or canceled run
	out := map[string]JobRunStats{}
	for _, r := range rows {
		st := out[r.JobID]
		switch RunStatus(r.Status) {
		case RunSucceeded:
			st.LastSuccessAt = r.FinishedAt
		case RunFailed:
			st.LastFailureAt = r.FinishedAt
		}
		out[r.JobID] = st
		if cur, ok := newest[r.JobID]; !ok || r.ID > cur.ID {
			newest[r.JobID] = r
		}
		if r.FinishedAt != nil {
			if cur, ok := finished[r.JobID]; !ok || r.ID > cur.ID {
				finished[r.JobID] = r
			}
		}
	}
	for id, st := range out {
		n := newest[id]
		st.LastStatus, st.LastRunAt = RunStatus(n.Status), n.at()
		if f, ok := finished[id]; ok && f.StartedAt != nil {
			d := f.FinishedAt.Sub(*f.StartedAt)
			st.LastDuration = &d
		}
		out[id] = st
	}
	return out
}
