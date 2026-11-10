package openbao

import (
	"context"
	"maps"
	"sync"
	"time"
)

const (
	maxCacheEntries = 1024
)

type cacheEntry struct {
	fields  map[string]string
	expires time.Time
	gen     uint64
}

// cache is a short-lived copy of secret fields for the dispatch path. A write
// or delete through this controller evicts the entry and bumps one global
// generation, so a read that started earlier cannot store a stale value after
// it (a write storm only costs cache hits, never correctness). Writes
// through other controllers, or straight into OpenBao, can be stale for up to
// the TTL.
type cache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*cacheEntry
	gen     uint64 // bumped by every write through this controller

	cancel context.CancelFunc
	done   chan struct{}
}

func newCache(ttl time.Duration, now func() time.Time) *cache {
	return &cache{ttl: ttl, now: now, entries: map[string]*cacheEntry{}}
}

func key(scope, name string) string { return scope + "/" + name }

// forget evicts scope/name and invalidates reads in flight. It is safe on a
// nil cache.
func (c *cache) forget(scope, name string) {
	if c == nil {
		return
	}
	k := key(scope, name)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	if e, ok := c.entries[k]; ok {
		clear(e.fields)
		delete(c.entries, k)
	}
}

// get returns a copy of the cached fields, or loads them. The caller clears the
// copy; the cached map stays intact.
func (c *cache) get(ctx context.Context, scope, name string, load func(context.Context, string, string) (map[string]string, error)) (map[string]string, error) {
	k := key(scope, name)
	c.mu.Lock()
	if e, ok := c.entries[k]; ok && c.now().Before(e.expires) {
		out := maps.Clone(e.fields)
		c.mu.Unlock()
		return out, nil
	}
	gen := c.gen
	c.mu.Unlock()

	fields, err := load(ctx, scope, name)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen == gen {
		c.store(k, maps.Clone(fields), gen)
	}
	return fields, nil
}

// store inserts an entry, evicting the oldest when full. The caller holds mu.
func (c *cache) store(k string, fields map[string]string, gen uint64) {
	if len(c.entries) >= maxCacheEntries {
		var oldest string
		var at time.Time
		for ek, e := range c.entries {
			if oldest == "" || e.expires.Before(at) {
				oldest, at = ek, e.expires
			}
		}
		clear(c.entries[oldest].fields)
		delete(c.entries, oldest)
	}
	c.entries[k] = &cacheEntry{fields: fields, expires: c.now().Add(c.ttl), gen: gen}
}

// start runs the sweeper that drops expired entries every TTL. It clears the
// maps, which only drops references: Go strings cannot be zeroed.
func (c *cache) start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(context.WithoutCancel(ctx))
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		t := time.NewTicker(c.ttl)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.sweep()
			}
		}
	}()
}

func (c *cache) sweep() {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			clear(e.fields)
			delete(c.entries, k)
		}
	}
}

func (c *cache) stop(ctx context.Context) {
	if c.cancel == nil {
		return
	}
	c.cancel()
	select {
	case <-c.done:
	case <-ctx.Done():
	}
	c.mu.Lock()
	for k, e := range c.entries {
		clear(e.fields)
		delete(c.entries, k)
	}
	c.mu.Unlock()
}
