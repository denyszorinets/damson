package logs

import (
	"container/list"
	"sync"
	"time"
)

// attemptCache maps attempt ids to execution ids for a short time so a busy
// agent does not cost one store query per batch. It is only a hint: entries
// expire after ttl, and the chunk insert is fenced by the store regardless.
// Unknown attempts are cached for a fifth of the ttl.
type attemptCache struct {
	mu    sync.Mutex
	cap   int
	ttl   time.Duration
	order *list.List // front is the most recently used
	items map[string]*list.Element
}

type cacheEntry struct {
	attempt string
	execID  string // empty: the attempt is unknown or stale
	expires time.Time
}

func newAttemptCache(capacity int, ttl time.Duration) *attemptCache {
	return &attemptCache{cap: capacity, ttl: ttl, order: list.New(), items: make(map[string]*list.Element)}
}

// get returns the cached execution id (empty for a cached stale attempt) and
// whether a live entry exists.
func (c *attemptCache) get(attempt string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[attempt]
	if !ok {
		return "", false
	}
	e := el.Value.(*cacheEntry)
	if !now.Before(e.expires) {
		c.removeLocked(el)
		return "", false
	}
	c.order.MoveToFront(el)
	return e.execID, true
}

// put records the lookup result; execID is empty for a stale attempt.
func (c *attemptCache) put(attempt, execID string, now time.Time) {
	ttl := c.ttl
	if execID == "" {
		ttl /= 5
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[attempt]; ok {
		c.removeLocked(el)
	}
	c.items[attempt] = c.order.PushFront(&cacheEntry{attempt: attempt, execID: execID, expires: now.Add(ttl)})
	for c.order.Len() > c.cap {
		c.removeLocked(c.order.Back())
	}
}

// forget drops one attempt, for callers that learned it is no longer current.
func (c *attemptCache) forget(attempt string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[attempt]; ok {
		c.removeLocked(el)
	}
}

func (c *attemptCache) removeLocked(el *list.Element) {
	delete(c.items, el.Value.(*cacheEntry).attempt)
	c.order.Remove(el)
}

func (c *attemptCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
