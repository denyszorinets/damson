package agent

import "time"

const (
	// tombstoneTTL is how long a cancel for an unknown attempt is remembered.
	tombstoneTTL = 15 * time.Minute
	// tombstoneMax bounds the memory; the oldest entry goes first.
	tombstoneMax = 4096
)

// tombstones remembers attempts cancelled before their step.execute arrived,
// so the late execute is refused instead of starting work nobody wants.
// Request handlers run concurrently, so this order is possible. The owner
// serializes access.
type tombstones struct {
	at    map[string]time.Time
	order []string
}

func (t *tombstones) add(attempt string, now time.Time) {
	if t.at == nil {
		t.at = map[string]time.Time{}
	}
	t.prune(now)
	if _, ok := t.at[attempt]; ok {
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
