package storetest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func acquire(t *testing.T, s store.Store, holder string, now time.Time, ttl time.Duration) (store.Lease, bool) {
	t.Helper()
	l, ok, err := s.AcquireLeader(context.Background(), "reaper", holder, now, now.Add(ttl))
	if err != nil {
		t.Fatalf("AcquireLeader(%s): %v", holder, err)
	}
	return l, ok
}

func checkLeader(s store.Store, holder string, term int64, now time.Time) error {
	return s.WithTx(context.Background(), func(tx store.Tx) error {
		return tx.CheckLeader(context.Background(), "reaper", holder, term, now)
	})
}

func testLeaderLease(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	l1, ok := acquire(t, s, "a", t0, 10*time.Second)
	if !ok || l1.Term != 1 || !l1.ExpiresAt.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("first acquire = %+v, %v; want term 1", l1, ok)
	}
	if _, ok := acquire(t, s, "b", t0.Add(5*time.Second), 10*time.Second); ok {
		t.Fatal("b acquired a valid lease of a")
	}
	// A renewal by the holder extends the expiry and keeps the term.
	l2, ok := acquire(t, s, "a", t0.Add(5*time.Second), 10*time.Second)
	if !ok || l2.Term != 1 || !l2.ExpiresAt.Equal(t0.Add(15*time.Second)) {
		t.Fatalf("renew = %+v, %v", l2, ok)
	}
	if err := checkLeader(s, "a", 1, t0.Add(6*time.Second)); err != nil {
		t.Fatalf("CheckLeader of the holder: %v", err)
	}
	if err := checkLeader(s, "a", 2, t0.Add(6*time.Second)); !errors.Is(err, store.ErrStale) {
		t.Fatalf("CheckLeader wrong term: %v; want ErrStale", err)
	}
	if err := checkLeader(s, "b", 1, t0.Add(6*time.Second)); !errors.Is(err, store.ErrStale) {
		t.Fatalf("CheckLeader wrong holder: %v; want ErrStale", err)
	}
	if err := checkLeader(s, "a", 1, t0.Add(15*time.Second)); !errors.Is(err, store.ErrStale) {
		t.Fatalf("CheckLeader at expiry: %v; want ErrStale", err)
	}
	// After the expiry another holder takes over with the next term, and the old
	// holder can neither renew nor pass the fence.
	l3, ok := acquire(t, s, "b", t0.Add(16*time.Second), 10*time.Second)
	if !ok || l3.Term != 2 {
		t.Fatalf("takeover = %+v, %v; want term 2", l3, ok)
	}
	if _, ok := acquire(t, s, "a", t0.Add(17*time.Second), 10*time.Second); ok {
		t.Fatal("deposed holder renewed")
	}
	if err := checkLeader(s, "a", 1, t0.Add(17*time.Second)); !errors.Is(err, store.ErrStale) {
		t.Fatalf("deposed holder passes the fence: %v", err)
	}
	// Release by a non-holder or a stale term does nothing.
	if err := s.ReleaseLeader(ctx, "reaper", "a", 1, t0.Add(18*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLeader(ctx, "reaper", "b", 1, t0.Add(18*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := checkLeader(s, "b", 2, t0.Add(18*time.Second)); err != nil {
		t.Fatalf("lease lost to a foreign release: %v", err)
	}
	// Release by the holder frees it at once; the term is kept, so the next
	// holder (even the same one) gets term 3.
	if err := s.ReleaseLeader(ctx, "reaper", "b", 2, t0.Add(19*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := checkLeader(s, "b", 2, t0.Add(19*time.Second)); !errors.Is(err, store.ErrStale) {
		t.Fatalf("released lease still fences: %v", err)
	}
	l4, ok := acquire(t, s, "c", t0.Add(20*time.Second), 10*time.Second)
	if !ok || l4.Term != 3 {
		t.Fatalf("acquire after release = %+v, %v; want term 3", l4, ok)
	}
	// A lease is free at exactly its expiry instant.
	l5, ok := acquire(t, s, "d", t0.Add(30*time.Second), 10*time.Second)
	if !ok || l5.Term != 4 {
		t.Fatalf("acquire at the expiry instant = %+v, %v; want term 4", l5, ok)
	}
	// Leases are per name.
	if _, ok, err := s.AcquireLeader(ctx, "other", "a", t0, t0.Add(time.Second)); err != nil || !ok {
		t.Fatalf("other duty = %v, %v", ok, err)
	}
}

func testLeaderAcquireRace(t *testing.T, s store.Store) {
	migrated(t, s)
	const n = 8
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := s.AcquireLeader(context.Background(), "reaper", string(rune('a'+i)), t0, t0.Add(time.Minute))
			if err != nil {
				t.Errorf("AcquireLeader: %v", err)
				return
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d holders acquired the lease; want exactly 1", wins.Load())
	}
}

func testDeleteDeadControllers(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	for _, id := range []string{"old", "live"} {
		if err := s.RegisterControllerInstance(ctx, id, "http://"+id, t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.HeartbeatControllerInstance(ctx, "live", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	n, err := s.DeleteDeadControllerInstances(ctx, t0.Add(time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("DeleteDeadControllerInstances = %d, %v; want 1", n, err)
	}
	list, err := s.ListControllerInstances(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "live" {
		t.Fatalf("instances = %+v, %v; want only live", list, err)
	}
}
