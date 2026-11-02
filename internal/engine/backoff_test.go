package engine

import (
	"fmt"
	"testing"
	"time"
)

func TestBackoffDelay(t *testing.T) {
	p := backoffParams{initial: 5 * time.Second, max: time.Minute, multiplier: 2, jitter: 0}
	tests := []struct {
		name string
		p    backoffParams
		n    int
		u    float64
		want time.Duration
	}{
		{"first retry waits initial", p, 1, 0, 5 * time.Second},
		{"doubles", p, 2, 0, 10 * time.Second},
		{"doubles again", p, 3, 0, 20 * time.Second},
		{"capped at max", p, 5, 0, time.Minute},
		{"stays capped", p, 50, 0, time.Minute},
		{"huge attempt does not overflow", p, 1_000_000, 0, time.Minute},
		{"multiplier 1 is constant", backoffParams{initial: 3 * time.Second, max: time.Hour, multiplier: 1}, 9, 0, 3 * time.Second},
		{"jitter at u=0 is the full delay", backoffParams{initial: 10 * time.Second, max: time.Hour, multiplier: 1, jitter: 0.2}, 1, 0, 10 * time.Second},
		{"jitter at u=0.5", backoffParams{initial: 10 * time.Second, max: time.Hour, multiplier: 1, jitter: 0.2}, 1, 0.5, 9 * time.Second},
		{"full jitter at u=0.5", backoffParams{initial: 10 * time.Second, max: time.Hour, multiplier: 1, jitter: 1}, 1, 0.5, 5 * time.Second},
	}
	for _, tc := range tests {
		if got := backoffDelay(tc.p, tc.n, tc.u); got != tc.want {
			t.Errorf("%s: backoffDelay(n=%d, u=%v) = %v; want %v", tc.name, tc.n, tc.u, got, tc.want)
		}
	}
	// Jitter only shortens: the delay stays within [(1-jitter)*base, base].
	jp := backoffParams{initial: 10 * time.Second, max: time.Minute, multiplier: 2, jitter: 0.3}
	for n := 1; n <= 8; n++ {
		full := backoffDelay(backoffParams{initial: jp.initial, max: jp.max, multiplier: 2}, n, 0)
		for _, u := range []float64{0, 0.25, 0.5, 0.999999} {
			got := backoffDelay(jp, n, u)
			if got > full || got < time.Duration(float64(full)*0.7)-time.Nanosecond {
				t.Errorf("n=%d u=%v: %v outside [%v, %v]", n, u, got, time.Duration(float64(full)*0.7), full)
			}
		}
	}
}

func TestBackoffDefaults(t *testing.T) {
	p := backoffOf(&Retry{MaxAttempts: 3})
	if p.initial != time.Second || p.multiplier != 2 || p.max != 5*time.Minute || p.jitter != 0.2 {
		t.Fatalf("defaults = %+v; want 1s, x2, 5m, 0.2", p)
	}
	zero := 0.0
	one := 1.0
	p = backoffOf(&Retry{MaxAttempts: 3, Backoff: &Backoff{Initial: "2s", Max: "1s", Multiplier: &one, Jitter: &zero}})
	if p.jitter != 0 || p.multiplier != 1 {
		t.Fatalf("explicit zero jitter and multiplier 1 were replaced by defaults: %+v", p)
	}
	if p.max < p.initial {
		t.Fatalf("max %v below initial %v", p.max, p.initial)
	}
}

func TestJitterUnit(t *testing.T) {
	if a, b := jitterUnit("run-1", "step", 2), jitterUnit("run-1", "step", 2); a != b {
		t.Fatalf("not deterministic: %v != %v", a, b)
	}
	seen := map[float64]bool{}
	var sum float64
	const n = 2000
	for i := range n {
		u := jitterUnit(fmt.Sprintf("run-%d", i), "step", 1+i%7)
		if u < 0 || u >= 1 {
			t.Fatalf("u = %v outside [0, 1)", u)
		}
		seen[u] = true
		sum += u
	}
	if len(seen) < n-5 {
		t.Fatalf("only %d distinct values in %d keys", len(seen), n)
	}
	if mean := sum / n; mean < 0.45 || mean > 0.55 {
		t.Fatalf("mean = %v; want about 0.5", mean)
	}
	// Different runs, steps and attempts spread: the key parts do not collide.
	if jitterUnit("a", "bc", 1) == jitterUnit("ab", "c", 1) {
		t.Fatal("run and path are not separated")
	}
}
