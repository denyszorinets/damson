package inventory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// fakeNodes is a NodeStore whose ApplyNodeDiff fails a set number of times.
type fakeNodes struct {
	store.NodeStore
	failures int
	err      error
	applies  int
}

func (f *fakeNodes) ListNodes(context.Context, store.NodeFilter) ([]store.Node, error) {
	return nil, nil
}
func (f *fakeNodes) FindNodeByName(context.Context, string) (store.Node, error) {
	return store.Node{}, store.ErrNotFound
}
func (f *fakeNodes) ApplyNodeDiff(context.Context, store.NodeDiff, time.Time) error {
	f.applies++
	if f.applies <= f.failures {
		return f.err
	}
	return nil
}

func TestSyncRetriesAndGivesUp(t *testing.T) {
	ds := []store.Node{{Name: "a", Address: "a.example.com", Port: 22, Transport: "ssh", HostKeyPolicy: "strict", Enabled: true}}
	f := &fakeNodes{failures: 1, err: store.ErrStale}
	if _, err := Sync(context.Background(), f, "s1", ds, false, false, time.Now()); err != nil || f.applies != 2 {
		t.Fatalf("one stale then success: err %v after %d applies", err, f.applies)
	}
	f = &fakeNodes{failures: 100, err: store.ErrConflict}
	_, err := Sync(context.Background(), f, "s1", ds, false, false, time.Now())
	if !errors.Is(err, store.ErrConflict) || f.applies != 3 {
		t.Fatalf("always failing: err %v after %d applies; want 3 attempts and ErrConflict", err, f.applies)
	}
	f = &fakeNodes{failures: 100, err: errors.New("disk on fire")}
	if _, err = Sync(context.Background(), f, "s1", ds, false, false, time.Now()); err == nil || f.applies != 1 {
		t.Fatalf("other errors are not retried: err %v after %d applies", err, f.applies)
	}
	if _, err = Sync(context.Background(), f, "manual", ds, true, false, time.Now()); !errors.Is(err, ErrBadDesired) {
		t.Fatalf("the manual source must be refused: %v", err)
	}
}
