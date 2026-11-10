package store

import (
	"context"
	"time"
)

// MetricsSnapshot is the cluster-wide state /metrics reports. It comes from
// the database, so any controller answers with the same numbers.
type MetricsSnapshot struct {
	RunsByStatus map[string]int64
	// Executions holds the queued, dispatched and running executions per pool.
	Executions []PoolExecutions
	// Agents holds the connected agents and their total slots per pool.
	Agents []PoolAgents
	// LogDeletesPending is the size of the log object delete queue.
	LogDeletesPending int64
}

// PoolExecutions counts the active executions of one pool by state.
type PoolExecutions struct {
	Pool, State string
	Count       int64
}

// PoolAgents counts the connected agents of one pool and their capacity.
type PoolAgents struct {
	Pool   string
	Agents int64
	Slots  int64
}

// MetricsStore reads the numbers behind /metrics.
type MetricsStore interface {
	// seenAfter is the oldest last_seen a connected agent may have to count.
	MetricsSnapshot(ctx context.Context, seenAfter time.Time) (MetricsSnapshot, error)
}
