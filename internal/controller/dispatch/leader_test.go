package dispatch_test

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/store"
)

// fakeLeader reports a settable term.
type fakeLeader struct {
	term atomic.Int64 // 0 means not leader
}

func (f *fakeLeader) Term() (int64, bool) { t := f.term.Load(); return t, t > 0 }

func TestReaperOnlyRunsForTheLeader(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		start := time.Now()
		ex := claimed(t, st, start)
		expired := start.Add(2 * time.Minute)
		lead := &fakeLeader{}
		d, err := dispatch.New(dispatch.Config{
			Store: st, Agents: noAgents{}, Plugins: noPlugins{}, Advance: &recordEnqueuer{}, ControllerID: "ctl-1",
			Now: func() time.Time { return expired }, Logger: slog.New(slog.DiscardHandler), ReapInterval: 20 * time.Millisecond, Leader: lead,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Stop(ctx) })

		// Not the leader: the expired lease is left alone.
		time.Sleep(150 * time.Millisecond)
		if got, _ := st.GetExecution(ctx, ex.ID); got.State != store.ExecRunning {
			t.Fatalf("non-leader reaped: state = %s", got.State)
		}
		// Claims term 5 while the table says ctl-1 holds nothing: the fence
		// rolls the reap back.
		lead.term.Store(5)
		time.Sleep(150 * time.Millisecond)
		if got, _ := st.GetExecution(ctx, ex.ID); got.State != store.ExecRunning {
			t.Fatalf("reaped under a stale term: state = %s", got.State)
		}
		// Holding the lease for real at that term lets the reap through.
		if _, ok, err := st.AcquireLeader(ctx, dispatch.LeaderReaper, "ctl-1", expired, expired.Add(time.Minute)); err != nil || !ok {
			t.Fatalf("AcquireLeader = %v, %v", ok, err)
		}
		lead.term.Store(1)
		waitFor(t, func() bool { got, _ := st.GetExecution(ctx, ex.ID); return got.State == store.ExecLost })
	})
}

func TestLeaderRemovesDeadControllers(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		now := time.Now()
		for id, seen := range map[string]time.Time{"dead": now.Add(-time.Hour), "live": now} {
			if err := st.RegisterControllerInstance(ctx, id, "http://"+id, seen); err != nil {
				t.Fatal(err)
			}
		}
		lead := &fakeLeader{}
		lead.term.Store(1)
		if _, ok, err := st.AcquireLeader(ctx, dispatch.LeaderReaper, "ctl-1", now, now.Add(time.Hour)); err != nil || !ok {
			t.Fatalf("AcquireLeader = %v, %v", ok, err)
		}
		d, err := dispatch.New(dispatch.Config{
			Store: st, Agents: noAgents{}, Plugins: noPlugins{}, Advance: &recordEnqueuer{}, ControllerID: "ctl-1",
			Now: func() time.Time { return now }, Logger: slog.New(slog.DiscardHandler), ReapInterval: 20 * time.Millisecond, Leader: lead,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Stop(ctx) })
		waitFor(t, func() bool {
			l, _ := st.ListControllerInstances(ctx)
			return len(l) == 1 && l[0].ID == "live"
		})
	})
}
