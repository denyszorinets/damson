package store

import (
	"context"
	"time"
)

// Lease is a held leader lease. Term grows each time the lease changes hands
// and never resets, so it fences the actions of a deposed leader.
type Lease struct {
	Term      int64
	ExpiresAt time.Time
}

// LeaderStore persists the leader leases of singleton duties (ARCHITECTURE
// 8.4). Time is the caller's injected clock, as for execution leases.
type LeaderStore interface {
	// AcquireLeader acquires or renews the lease name for holder until
	// expiresAt. It succeeds when holder already has it or the current one
	// expired at or before now; ok is false when another holder's lease is
	// still valid. A takeover bumps the term, a renewal keeps it.
	AcquireLeader(ctx context.Context, name, holder string, now, expiresAt time.Time) (lease Lease, ok bool, err error)
	// ReleaseLeader expires the lease early if holder still has it at term.
	// Releasing a lease that was lost or already expired is not an error.
	ReleaseLeader(ctx context.Context, name, holder string, term int64, now time.Time) error
	// DeleteDeadControllerInstances removes the registry rows of controllers
	// that crashed without deregistering: last_seen before cutoff.
	DeleteDeadControllerInstances(ctx context.Context, cutoff time.Time) (int64, error)
}
