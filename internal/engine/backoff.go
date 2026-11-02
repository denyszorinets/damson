package engine

import (
	"hash/fnv"
	"math"
	"time"
)

// Retry defaults (ADR-0036).
const (
	defaultBackoffInitial    = time.Second
	defaultBackoffMultiplier = 2.0
	defaultBackoffMax        = 5 * time.Minute
	defaultBackoffJitter     = 0.2
)

// backoffParams are a retry block's resolved delay settings.
type backoffParams struct {
	initial, max time.Duration
	multiplier   float64
	jitter       float64
}

// backoffOf resolves r's backoff with the defaults. The spec was validated,
// so the strings parse; a value that does not falls back to the default.
func backoffOf(r *Retry) backoffParams {
	p := backoffParams{initial: defaultBackoffInitial, max: defaultBackoffMax, multiplier: defaultBackoffMultiplier, jitter: defaultBackoffJitter}
	b := r.Backoff
	if b == nil {
		return p
	}
	if d, err := time.ParseDuration(b.Initial); err == nil && d > 0 {
		p.initial = d
	}
	if d, err := time.ParseDuration(b.Max); err == nil && d > 0 {
		p.max = d
	}
	if b.Multiplier != nil {
		p.multiplier = *b.Multiplier
	}
	if b.Jitter != nil {
		p.jitter = *b.Jitter
	}
	p.max = max(p.max, p.initial)
	return p
}

// backoffDelay is the wait before the retry that follows failed attempt n
// (n >= 1): initial * multiplier^(n-1), capped at max, then reduced by
// jitter*u, so it lies in [(1-jitter)*base, base] and never exceeds max. u is
// the injected randomness in [0, 1), see jitterUnit. The curve is computed
// step by step and stops at the cap, so a large n cannot overflow.
func backoffDelay(p backoffParams, n int, u float64) time.Duration {
	base := float64(p.initial)
	limit := float64(p.max)
	for i := 1; i < n && base < limit; i++ {
		base *= p.multiplier
	}
	base = math.Min(base, limit)
	return time.Duration(base * (1 - p.jitter*u))
}

// jitterUnit maps (run, step path, attempt) to a number in [0, 1). The engine
// has no randomness (ADR-0009): the jitter is a hash of what identifies the
// retry, so the same state always gives the same schedule, controllers racing
// on one run emit identical actions, and different steps and runs still spread.
func jitterUnit(runID, path string, attempt int) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(runID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(path))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte{byte(attempt >> 24), byte(attempt >> 16), byte(attempt >> 8), byte(attempt)})
	return float64(h.Sum64()>>11) / (1 << 53)
}
