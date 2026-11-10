package agent

import (
	"slices"
	"time"
)

const (
	// tombstoneTTL is how long a cancel for an unknown attempt is remembered.
	tombstoneTTL = 15 * time.Minute
	// tombstoneMax bounds the memory; the oldest entry goes first.
	tombstoneMax = 4096
)

// tombstones remembers attempt ids for a while: those cancelled before their
// step.execute arrived (request handlers run concurrently, so this order is
// possible) and those already completed, so a late or retried execute is
// refused instead of running the step again. The owner serializes access.
type tombstones struct {
	at    map[string]time.Time
	order []string
}

func (t *tombstones) add(attempt string, now time.Time) {
	if t.at == nil {
		t.at = map[string]time.Time{}
	}
	t.prune(now)
	if _, ok := t.at[attempt]; ok { // refresh: move to the newest position
		t.order = slices.DeleteFunc(t.order, func(id string) bool { return id == attempt })
		t.at[attempt] = now
		t.order = append(t.order, attempt)
		return
	}
	for len(t.order) >= tombstoneMax {
		delete(t.at, t.order[0])
		t.order = t.order[1:]
	}
	t.at[attempt] = now
	t.order = append(t.order, attempt)
}

func (t *tombstones) has(attempt string, now time.Time) bool {
	t.prune(now)
	_, ok := t.at[attempt]
	return ok
}

func (t *tombstones) prune(now time.Time) {
	for len(t.order) > 0 && now.Sub(t.at[t.order[0]]) > tombstoneTTL {
		delete(t.at, t.order[0])
		t.order = t.order[1:]
	}
}
