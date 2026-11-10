package advance

import (
	"context"
	"sync"
)

// Hub fans out "this run changed" signals to in-process subscribers, such as
// the SSE stream of a run. Subscribers still poll, so a missed signal only
// delays an event. It satisfies api.RunNotifier structurally.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

// NewHub returns an empty Hub.
func NewHub() *Hub { return &Hub{subs: map[string]map[chan struct{}]struct{}{}} }

// Subscribe returns a channel that receives a coalesced value per change of
// runID, and a release function that must be called to unsubscribe. The
// subscription also ends when ctx is done.
func (h *Hub) Subscribe(ctx context.Context, runID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[runID] == nil {
		h.subs[runID] = map[chan struct{}]struct{}{}
	}
	h.subs[runID][ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(h.subs[runID], ch)
			if len(h.subs[runID]) == 0 {
				delete(h.subs, runID)
			}
		})
	}
	stop := context.AfterFunc(ctx, release)
	return ch, func() { stop(); release() }
}

// Notify wakes every subscriber of runID without blocking.
func (h *Hub) Notify(runID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[runID] {
		select {
		case ch <- struct{}{}:
		default: // already pending: coalesced
		}
	}
}
