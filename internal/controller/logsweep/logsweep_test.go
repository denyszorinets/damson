package logsweep

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/denyszorinets/damson/internal/logblob"
	"github.com/denyszorinets/damson/internal/store"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type queue struct {
	mu      sync.Mutex
	entries map[string]store.LogObjectDelete
}

func newQueue(es ...store.LogObjectDelete) *queue {
	q := &queue{entries: map[string]store.LogObjectDelete{}}
	for _, e := range es {
		q.entries[e.ID] = e
	}
	return q
}

func (q *queue) ListDueLogObjectDeletes(_ context.Context, now time.Time, _ int) ([]store.LogObjectDelete, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []store.LogObjectDelete
	for _, e := range q.entries {
		if !e.NotBefore.After(now) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (q *queue) DeferLogObjectDelete(_ context.Context, d store.LogObjectDelete, next time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.entries[d.ID]
	if !ok || !e.NotBefore.Equal(d.NotBefore) {
		return store.ErrStale
	}
	e.NotBefore, e.Tries = next, e.Tries+1
	q.entries[d.ID] = e
	return nil
}

func (q *queue) CompleteLogObjectDelete(_ context.Context, d store.LogObjectDelete) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.entries[d.ID]
	if !ok || !e.NotBefore.Equal(d.NotBefore) {
		return store.ErrStale
	}
	delete(q.entries, d.ID)
	return nil
}

func (q *queue) CountLogObjectDeletes(context.Context) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return int64(len(q.entries)), nil
}

type fakeBlob struct {
	mu       sync.Mutex
	prefixes []string
	err      error
}

func (f *fakeBlob) Put(context.Context, string, []byte) error   { return nil }
func (f *fakeBlob) Get(context.Context, string) ([]byte, error) { return nil, store.ErrNotFound }
func (f *fakeBlob) DeletePrefix(_ context.Context, p string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	f.prefixes = append(f.prefixes, p)
	return 1, nil
}

var _ logblob.Blob = (*fakeBlob)(nil)

func entry(id, prefix string) store.LogObjectDelete {
	return store.LogObjectDelete{ID: id, Prefix: prefix, NotBefore: t0}
}

func sweeper(t *testing.T, q *queue, bs ...logblob.Blob) *Sweeper {
	t.Helper()
	s, err := New(Config{Store: q, Backends: bs, Now: func() time.Time { return t0.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDeletesAndCompletes(t *testing.T) {
	q := newQueue(entry("1", "logs/e1/"), entry("2", "logs/e2/a1/"))
	b := &fakeBlob{}
	if n := sweeper(t, q, b).Once(context.Background()); n != 2 {
		t.Fatalf("completed %d, want 2", n)
	}
	if c, _ := q.CountLogObjectDeletes(context.Background()); c != 0 || len(b.prefixes) != 2 {
		t.Fatalf("queue %d, deleted %v", c, b.prefixes)
	}
}

func TestFailureBacksOff(t *testing.T) {
	q := newQueue(entry("1", "logs/e1/"))
	b := &fakeBlob{err: errors.New("boom")}
	s := sweeper(t, q, b)
	if n := s.Once(context.Background()); n != 0 {
		t.Fatalf("completed %d on failure", n)
	}
	e := q.entries["1"]
	if e.Tries != 1 || !e.NotBefore.After(t0.Add(time.Minute)) {
		t.Fatalf("entry after failure = %+v", e)
	}
	if n := s.Once(context.Background()); n != 0 || q.entries["1"].Tries != 1 {
		t.Fatal("a backing-off entry was retried at once")
	}
}

func TestTwoSweepersDeleteOnce(t *testing.T) {
	q := newQueue(entry("1", "logs/e1/"))
	b := &fakeBlob{}
	s1, s2 := sweeper(t, q, b), sweeper(t, q, b)
	var wg sync.WaitGroup
	for _, s := range []*Sweeper{s1, s2} {
		wg.Add(1)
		go func() { defer wg.Done(); s.Once(context.Background()) }()
	}
	wg.Wait()
	if len(b.prefixes) != 1 {
		t.Fatalf("deleted %d times: %v", len(b.prefixes), b.prefixes)
	}
}

func TestInvalidPrefixIsNotDeleted(t *testing.T) {
	q := newQueue(entry("1", "logs/"), entry("2", ""), entry("3", "logs/../"))
	b := &fakeBlob{}
	sweeper(t, q, b).Once(context.Background())
	if len(b.prefixes) != 0 {
		t.Fatalf("deleted %v", b.prefixes)
	}
}

func TestNoBackendsKeepsEntries(t *testing.T) {
	q := newQueue(entry("1", "logs/e1/"))
	if n := sweeper(t, q).Once(context.Background()); n != 0 {
		t.Fatalf("completed %d", n)
	}
	if c, _ := q.CountLogObjectDeletes(context.Background()); c != 1 || q.entries["1"].Tries != 0 {
		t.Fatalf("a sweeper without backends touched the queue: %+v", q.entries)
	}
}

func TestClaimUsesTheTimeOfTheEntry(t *testing.T) {
	q := newQueue(entry("1", "logs/e1/"), entry("2", "logs/e2/"))
	now := t0.Add(time.Second)
	s, err := New(Config{Store: q, Backends: []logblob.Blob{&fakeBlob{}}, Now: func() time.Time { now = now.Add(10 * time.Minute); return now }})
	if err != nil {
		t.Fatal(err)
	}
	// Defer both so the claim times stay visible: make the delete fail.
	s.cfg.Backends = []logblob.Blob{&fakeBlob{err: errors.New("boom")}}
	s.Once(context.Background())
	a, b := q.entries["1"].NotBefore, q.entries["2"].NotBefore
	if a.Equal(b) {
		t.Fatalf("both entries were claimed from one clock reading: %v", a)
	}
}

func TestStartStop(t *testing.T) {
	q := newQueue(entry("1", "logs/e1/"))
	s, err := New(Config{Store: q, Backends: []logblob.Blob{&fakeBlob{}}, Interval: 10 * time.Millisecond, Now: func() time.Time { return t0.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, _ := q.CountLogObjectDeletes(context.Background()); c == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c, _ := q.CountLogObjectDeletes(context.Background()); c != 0 {
		t.Fatalf("queue %d after run", c)
	}
}
