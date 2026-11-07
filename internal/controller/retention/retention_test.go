package retention_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/denyszorinets/damson/internal/controller/leader"
	"github.com/denyszorinets/damson/internal/controller/retention"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		if err := goleak.Find(); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func open(t *testing.T) *sqlite.Store {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

// lead starts an elector for holder and waits until it leads.
func lead(t *testing.T, st store.LeaderStore, holder string) *leader.Elector {
	t.Helper()
	el, err := leader.New(leader.Config{Store: st, Name: retention.LeaderName, Holder: holder, Renew: 20 * time.Millisecond, TTL: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := el.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = el.Stop(context.Background()) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := el.Term(); ok {
			return el
		}
		if time.Now().After(deadline) {
			t.Fatal("never became leader")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newDuty(t *testing.T, st *sqlite.Store, el retention.Leader, holder string, days int, now time.Time) *retention.Retention {
	t.Helper()
	r, err := retention.New(retention.Config{Store: st, Leader: el, Holder: holder, AuditDays: days, Now: func() time.Time { return now }, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func seed(t *testing.T, st store.AuditStore, now time.Time, ages ...int) {
	t.Helper()
	for i, days := range ages {
		ev := store.AuditEvent{ID: fmt.Sprintf("e-%d", i), At: now.AddDate(0, 0, -days), Actor: "a", Action: "secret.update", TargetType: store.AuditTargetSecret, Target: "t"}
		if err := st.AppendAuditEvent(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOncePrunesOldEventsAndRecordsTheDeletion(t *testing.T) {
	st := open(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	seed(t, st, now, 10, 31, 40, 400)
	r := newDuty(t, st, lead(t, st, "c1"), "c1", 30, now)
	r.Once(context.Background())
	evs, err := st.ListAuditEvents(context.Background(), store.AuditFilter{}, 50)
	if err != nil || len(evs) != 2 {
		t.Fatalf("events = %+v, %v; want the 10-day event and the prune event", evs, err)
	}
	var prune *store.AuditEvent
	for i := range evs {
		if evs[i].Action == store.ActionAuditPrune {
			prune = &evs[i]
		}
	}
	if prune == nil || prune.Actor != retention.Actor {
		t.Fatalf("no prune event: %+v", evs)
	}
	var d store.AuditPruneDetails
	if err := json.Unmarshal(prune.Details, &d); err != nil || d.Deleted != 3 || !d.Before.Equal(now.AddDate(0, 0, -30)) {
		t.Fatalf("details = %s, %v", prune.Details, err)
	}
	// A second pass has nothing to delete and writes no event.
	r.Once(context.Background())
	if evs, _ = st.ListAuditEvents(context.Background(), store.AuditFilter{}, 50); len(evs) != 2 {
		t.Fatalf("second pass changed the trail: %+v", evs)
	}
}

func TestBacklogLargerThanOneBatch(t *testing.T) {
	st := open(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	ages := make([]int, 450)
	for i := range ages {
		ages[i] = 100 + i
	}
	seed(t, st, now, ages...)
	newDuty(t, st, lead(t, st, "c1"), "c1", 30, now).Once(context.Background())
	evs, _ := st.ListAuditEvents(context.Background(), store.AuditFilter{}, 200)
	var sum int64
	for _, e := range evs {
		if e.Action != store.ActionAuditPrune {
			t.Fatalf("an old event survived: %+v", e)
		}
		var d store.AuditPruneDetails
		_ = json.Unmarshal(e.Details, &d)
		sum += d.Deleted
	}
	if sum != 450 || len(evs) != 3 {
		t.Fatalf("deleted per events = %d in %d events; want 450 in 3", sum, len(evs))
	}
}

type notLeader struct{}

func (notLeader) Term() (int64, bool) { return 0, false }

type staleLeader struct{}

func (staleLeader) Term() (int64, bool) { return 99, true }

func TestOnlyTheLeaseHolderPrunes(t *testing.T) {
	st := open(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	seed(t, st, now, 400)
	newDuty(t, st, notLeader{}, "c2", 30, now).Once(context.Background())
	// A controller that believes it leads with a wrong term is fenced by the store.
	lead(t, st, "c1")
	newDuty(t, st, staleLeader{}, "c2", 30, now).Once(context.Background())
	if evs, _ := st.ListAuditEvents(context.Background(), store.AuditFilter{}, 10); len(evs) != 1 || evs[0].Action == store.ActionAuditPrune {
		t.Fatalf("a non-leader pruned: %+v", evs)
	}
}

func TestZeroDaysKeepsEverything(t *testing.T) {
	st := open(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	seed(t, st, now, 1, 4000)
	r := newDuty(t, st, lead(t, st, "c1"), "c1", 0, now)
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.Once(context.Background())
	if evs, _ := st.ListAuditEvents(context.Background(), store.AuditFilter{}, 50); len(evs) != 2 {
		t.Fatalf("events = %+v", evs)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLoopPrunesOnStartAndStops(t *testing.T) {
	st := open(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	seed(t, st, now, 100)
	r := newDuty(t, st, lead(t, st, "c1"), "c1", 7, now)
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err == nil {
		t.Error("a second Start must fail")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs, _ := st.ListAuditEvents(context.Background(), store.AuditFilter{}, 50)
		if len(evs) == 1 && evs[0].Action == store.ActionAuditPrune {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not pruned: %+v", evs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	st := open(t)
	for name, cfg := range map[string]retention.Config{
		"no store":      {Leader: notLeader{}, Holder: "h"},
		"no leader":     {Store: st, Holder: "h"},
		"no holder":     {Store: st, Leader: notLeader{}},
		"negative days": {Store: st, Leader: notLeader{}, Holder: "h", AuditDays: -1},
	} {
		if _, err := retention.New(cfg); err == nil {
			t.Errorf("%s must fail", name)
		}
	}
}
