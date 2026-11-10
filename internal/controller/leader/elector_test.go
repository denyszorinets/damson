package leader

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/denyszorinets/damson/internal/store"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// fakeStore is a one-row lease table with a settable error and a clock.
type fakeStore struct {
	mu       sync.Mutex
	holder   string
	term     int64
	expires  time.Time
	err      error
	released []string
}

func (f *fakeStore) AcquireLeader(_ context.Context, _, holder string, now, exp time.Time) (store.Lease, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return store.Lease{}, false, f.err
	}
	if f.holder != holder && now.Before(f.expires) {
		return store.Lease{}, false, nil
	}
	if f.holder != holder {
		f.term++
	}
	f.holder, f.expires = holder, exp
	return store.Lease{Term: f.term, ExpiresAt: exp}, true, nil
}

func (f *fakeStore) ReleaseLeader(_ context.Context, _, holder string, term int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, holder)
	if f.holder == holder && f.term == term {
		f.expires = time.Time{}
	}
	return nil
}

func (f *fakeStore) DeleteDeadControllerInstances(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (f *fakeStore) setErr(err error) { f.mu.Lock(); f.err = err; f.mu.Unlock() }

func newElector(t *testing.T, fs *fakeStore, holder string, clock *time.Time, mu *sync.Mutex) *Elector {
	t.Helper()
	e, err := New(Config{Store: fs, Name: "reaper", Holder: holder, TTL: 30 * time.Second, Renew: 10 * time.Second,
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return *clock }})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestElectorTermAndStepDown(t *testing.T) {
	var mu sync.Mutex
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fs := &fakeStore{}
	e := newElector(t, fs, "a", &clock, &mu)
	if _, ok := e.Term(); ok {
		t.Fatal("leader before the first tick")
	}
	e.tick(context.Background())
	if term, ok := e.Term(); !ok || term != 1 {
		t.Fatalf("Term = %d, %v; want 1, true", term, ok)
	}
	// The store fails: the lease runs out locally one renew interval early.
	fs.setErr(errors.New("db down"))
	mu.Lock()
	clock = clock.Add(19 * time.Second)
	mu.Unlock()
	e.tick(context.Background())
	if _, ok := e.Term(); !ok {
		t.Fatal("stepped down too early")
	}
	mu.Lock()
	clock = clock.Add(2 * time.Second) // 21s > ttl 30s - renew 10s
	mu.Unlock()
	if _, ok := e.Term(); ok {
		t.Fatal("still leader after the lease ran out")
	}
}

func TestElectorTakeoverAndLoss(t *testing.T) {
	var mu sync.Mutex
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fs := &fakeStore{}
	a, b := newElector(t, fs, "a", &clock, &mu), newElector(t, fs, "b", &clock, &mu)
	a.tick(context.Background())
	b.tick(context.Background())
	if _, ok := b.Term(); ok {
		t.Fatal("b leads while a's lease is valid")
	}
	mu.Lock()
	clock = clock.Add(31 * time.Second) // a's lease expired without renewal
	mu.Unlock()
	b.tick(context.Background())
	if term, ok := b.Term(); !ok || term != 2 {
		t.Fatalf("b = %d, %v; want term 2", term, ok)
	}
	a.tick(context.Background())
	if _, ok := a.Term(); ok {
		t.Fatal("a still leads after losing the lease")
	}
}

func TestElectorStartStopReleases(t *testing.T) {
	var mu sync.Mutex
	clock := time.Now()
	fs := &fakeStore{}
	e := newElector(t, fs, "a", &clock, &mu)
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err == nil {
		t.Fatal("second Start accepted")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := e.Term(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never became leader")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fs.released) != 1 || fs.released[0] != "a" {
		t.Fatalf("released = %v; want [a]", fs.released)
	}
	if _, ok := e.Term(); ok {
		t.Fatal("leader after Stop")
	}
}

func TestNewValidates(t *testing.T) {
	fs := &fakeStore{}
	for _, c := range []Config{{}, {Store: fs, Name: "x"}, {Store: fs, Name: "x", Holder: "h", TTL: time.Second, Renew: time.Second}} {
		if _, err := New(c); err == nil {
			t.Errorf("New(%+v) accepted", c)
		}
	}
}

func TestStartExclusive(t *testing.T) {
	fs := &fakeStore{}
	mk := func(holder string) *Elector {
		e, err := New(Config{Store: fs, Name: "only", Holder: holder, TTL: 600 * time.Millisecond, Renew: 200 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	a, b := mk("a"), mk("b")
	if err := a.StartExclusive(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	// a renews in the background, so b never gets it and gives up.
	if err := b.StartExclusive(context.Background(), 700*time.Millisecond); !errors.Is(err, ErrHeld) {
		t.Fatalf("b = %v; want ErrHeld", err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.StartExclusive(context.Background(), time.Second); err != nil {
		t.Fatalf("b after a released: %v", err)
	}
	if err := b.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
