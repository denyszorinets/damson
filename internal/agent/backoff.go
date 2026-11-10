package agent

import "time"

// Reconnect backoff bounds (ARCHITECTURE 7.3).
const (
	DefaultBackoffBase = time.Second
	DefaultBackoffMax  = 30 * time.Second
)

// BackoffDelay returns the wait before reconnect attempt number attempt
// (0 for the first retry) using exponential backoff with full jitter: a value
// drawn uniformly from [0, min(limit, base*2^attempt)]. r is a random number
// in [0, 1]; it is injected so the function is pure.
func BackoffDelay(attempt int, base, limit time.Duration, r float64) time.Duration {
	if base <= 0 || limit <= 0 {
		return 0
	}
	ceiling := limit
	attempt = max(attempt, 0)
	// base<<attempt overflows past 62 bits; once it would exceed limit, stop.
	if attempt < 62 && base <= limit>>attempt {
		ceiling = base << attempt
	}
	r = min(1, max(r, 0))
	return time.Duration(r * float64(ceiling))
}
