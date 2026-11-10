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
}
