package api

import (
	"sync"
	"time"
)

// Webhook rate limit defaults: a token bucket per token (ARCHITECTURE 12.3).
// The limit is per controller, held in memory: it protects the controller, it
// is not durable state.
const (
	defaultHookRate  = 5.0 // requests per second
	defaultHookBurst = 20
	maxHookBuckets   = 10_000
)

type bucket struct {
	tokens float64
	last   time.Time
}

// rateLimiter is a token bucket per key. Keys are SHA-256 hashes of presented
// tokens, so an unknown token is limited too without storing it. The map is
// bounded: past maxHookBuckets, idle full buckets are dropped first.
type rateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*bucket
}

func newRateLimiter(rate float64, burst int) *rateLimiter {
	return &rateLimiter{rate: rate, burst: float64(burst), buckets: map[string]*bucket{}}
}

// allow takes one token from key's bucket and reports whether there was one.
// When not, retry is how long until there is.
func (l *rateLimiter) allow(key string, now time.Time) (ok bool, retry time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, found := l.buckets[key]
	if !found {
		if len(l.buckets) >= maxHookBuckets {
			l.evict(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens = min(l.burst, b.tokens+el*l.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

// evict drops buckets that have refilled completely. If none has, it drops
// them all: better to forgive a burst than to grow without bound.
func (l *rateLimiter) evict(now time.Time) {
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) >= maxHookBuckets {
		clear(l.buckets)
	}
}
