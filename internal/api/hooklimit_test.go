package api

import (
	"fmt"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newRateLimiter(2, 3)
	for i := range 3 {
		if ok, _ := l.allow("k", now); !ok {
			t.Fatalf("request %d refused inside the burst", i)
		}
	}
	ok, retry := l.allow("k", now)
	if ok || retry != 500*time.Millisecond {
		t.Fatalf("allow = %v, retry %v; want refused, 500ms", ok, retry)
	}
	if ok, _ := l.allow("other", now); !ok {
		t.Fatal("another key shares the bucket")
	}
	if ok, _ := l.allow("k", now.Add(time.Second)); !ok {
		t.Fatal("no refill after a second")
	}
}

func TestRateLimiterIsBounded(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newRateLimiter(1, 2)
	for i := range maxHookBuckets + 500 {
		l.allow(fmt.Sprintf("k%d", i), now)
	}
	if n := len(l.buckets); n > maxHookBuckets {
		t.Fatalf("%d buckets, limit %d", n, maxHookBuckets)
	}
	// Full buckets are evicted first; a drained one survives.
	l2 := newRateLimiter(1, 2)
	l2.allow("drained", now)
	l2.allow("drained", now)
	for i := range maxHookBuckets {
		l2.allow(fmt.Sprintf("idle%d", i), now.Add(time.Hour))
	}
	l2.allow("new", now.Add(time.Hour))
	if len(l2.buckets) > maxHookBuckets {
		t.Fatalf("%d buckets", len(l2.buckets))
	}
}
