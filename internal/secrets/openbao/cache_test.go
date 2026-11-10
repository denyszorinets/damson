package openbao

import (
	"context"
	"fmt"
	"github.com/denyszorinets/damson/internal/secrets/openbao/baotest"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestCacheServesCopiesUntilExpiry(t *testing.T) {
	ck := &clock{t: tNow}
	c := newCache(10*time.Second, ck.now)
	loads := 0
	load := func(context.Context, string, string) (map[string]string, error) {
		loads++
		return map[string]string{"token": fmt.Sprint("v", loads)}, nil
	}
	a, _ := c.get(context.Background(), "p", "x", load)
	clear(a) // the dispatcher clears what it gets
	b, _ := c.get(context.Background(), "p", "x", load)
	if b["token"] != "v1" || loads != 1 {
		t.Fatalf("second read = %v after %d loads; the cache must hand out copies", b, loads)
	}
	ck.add(11 * time.Second)
	d, _ := c.get(context.Background(), "p", "x", load)
	if d["token"] != "v2" || loads != 2 {
		t.Fatalf("after expiry = %v after %d loads", d, loads)
	}
}

// A write that lands while a read is in flight must not be undone by that read.
func TestCacheForgetBeatsAReadInFlight(t *testing.T) {
	c := newCache(10*time.Second, time.Now)
	started, release := make(chan struct{}), make(chan struct{})
	load := func(context.Context, string, string) (map[string]string, error) {
		close(started)
		<-release
		return map[string]string{"token": "old"}, nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.get(context.Background(), "p", "x", load)
	}()
	<-started
	c.forget("p", "x") // Put or Delete through this controller
	close(release)
	<-done
	got, _ := c.get(context.Background(), "p", "x", func(context.Context, string, string) (map[string]string, error) {
		return map[string]string{"token": "new"}, nil
	})
	if got["token"] != "new" {
		t.Fatalf("the stale read repopulated the cache: %v", got)
	}
}

func TestCacheIsBounded(t *testing.T) {
	ck := &clock{t: tNow}
	c := newCache(10*time.Second, ck.now)
	for i := range maxCacheEntries + 50 {
		ck.add(time.Millisecond)
		_, _ = c.get(context.Background(), "p", fmt.Sprint("n", i), func(context.Context, string, string) (map[string]string, error) {
			return map[string]string{"token": "v"}, nil
		})
	}
	if n := len(c.entries); n > maxCacheEntries {
		t.Fatalf("%d entries, bound is %d", n, maxCacheEntries)
	}
}

func TestCacheSweeperStops(t *testing.T) {
	c := newCache(5*time.Millisecond, time.Now)
	c.start(context.Background())
	_, _ = c.get(context.Background(), "p", "x", func(context.Context, string, string) (map[string]string, error) {
		return map[string]string{"token": "v"}, nil
	})
	time.Sleep(30 * time.Millisecond)
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d entries survived the sweeper", n)
	}
	c.stop(context.Background())
}

// With the cache on, a Put through the backend is visible to the next dispatch read.
func TestBackendCacheSeesOwnWrites(t *testing.T) {
	srv := baotest.Start(t)
	_, b := start(t, func() Config { c := cfgFor(srv, rootAuth()); c.CacheTTL = 30 * time.Second; return c }())
	ctx := actor()
	if _, _, err := b.Put(ctx, "p", "x", "token", map[string]string{"token": "one"}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.GetForDispatch(ctx, "p", "x"); got["token"] != "one" {
		t.Fatalf("first = %v", got)
	}
	if _, _, err := b.Put(ctx, "p", "x", "token", map[string]string{"token": "two"}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.GetForDispatch(ctx, "p", "x"); got["token"] != "two" {
		t.Fatalf("after put = %v; the cache served a stale value", got)
	}
	if err := b.Delete(ctx, "p", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.GetForDispatch(ctx, "p", "x"); err == nil {
		t.Fatal("a deleted secret was served from the cache")
	}
}
