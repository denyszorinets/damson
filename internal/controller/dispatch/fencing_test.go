package dispatch_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/controller"
	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
)

type noAgents struct{}

func (noAgents) Agents() []controller.AgentView { return nil }
func (noAgents) Reserve(string, int) int        { return 0 }
func (noAgents) Release(string, int)            {}

type noPlugins struct{}

func (noPlugins) Resolve(string, pluginreg.Filter) (pluginreg.Resolution, error) {
	return pluginreg.Resolution{}, errors.New("no plugins")
}

type recordEnqueuer struct {
	mu   sync.Mutex
	runs []string
}

func (r *recordEnqueuer) Enqueue(_ context.Context, _ store.Tx, runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, runID)
	return nil
}

func (r *recordEnqueuer) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}

func newBare(t *testing.T, st store.Store, now func() time.Time) (*dispatch.Dispatcher, *recordEnqueuer) {
	t.Helper()
	enq := &recordEnqueuer{}
	d, err := dispatch.New(dispatch.Config{
		Store: st, Agents: noAgents{}, Plugins: noPlugins{}, Advance: enq, ControllerID: "ctl-1", Now: now,
		Logger: slog.New(slog.DiscardHandler), ReapInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d, enq
}

// claimed seeds a run with one execution claimed by agent a1 (running).
func claimed(t *testing.T, st store.Store, now time.Time) store.Execution {
	t.Helper()
	ctx := context.Background()
	_, jv, err := st.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "j", Spec: json.RawMessage(twoStepSpec), Checksum: "s"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRun(ctx, store.NewRun{
		JobID: jv.JobID, JobVersionID: jv.ID,
		Steps: []store.NewStepInstance{{Path: "s1", Plugin: "demo", Execution: &store.NewExecution{Pool: "default", ScheduledAt: now}}},
	}, now); err != nil {
		t.Fatal(err)
	}
	got, err := st.ClaimExecutions(ctx, store.ClaimParams{
		AgentID: "a1", ControllerID: "ctl-1", Pools: []string{"default"}, Limit: 1, LeaseTTL: time.Minute,
	}, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("claim = %v, %v", got, err)
	}
	if err := st.MarkExecutionRunning(ctx, got[0].ID, got[0].AttemptID, now); err != nil {
		t.Fatal(err)
	}
	return got[0]
}

func TestCompleteFencing(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		now := time.Now()
		d, enq := newBare(t, st, func() time.Time { return now })
		ex := claimed(t, st, now)
		report := func(agent, attempt, status string) protocol.StepCompleteAck {
			ack, err := d.Complete(ctx, agent, protocol.StepComplete{AttemptID: attempt, Status: status})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			return ack
		}

		// Unknown attempt, and a different agent's report: discarded.
		for _, c := range [][2]string{{"a1", "unknown"}, {"other-agent", ex.AttemptID}} {
			if ack := report(c[0], c[1], protocol.StatusSucceeded); !ack.Stale || !ack.Acknowledged {
				t.Fatalf("report %v ack = %+v; want acknowledged and stale", c, ack)
			}
		}
		if got, _ := st.GetExecution(ctx, ex.ID); got.State != store.ExecRunning || enq.count() != 0 {
			t.Fatalf("a discarded report changed state to %s or enqueued %d", got.State, enq.count())
		}

		if ack := report("a1", ex.AttemptID, protocol.StatusSucceeded); !ack.Acknowledged || ack.Stale {
			t.Fatalf("ack = %+v; want acknowledged", ack)
		}
		if enq.count() != 1 {
			t.Fatalf("run.advance enqueued %d times; want 1", enq.count())
		}
		step, _ := st.GetStepInstance(ctx, ex.StepInstanceID)
		if step.Status != store.StepSucceeded {
			t.Fatalf("step = %s; want succeeded", step.Status)
		}

		// A duplicate (the agent retries until acknowledged) and a conflicting
		// late report are discarded and change nothing.
		for _, status := range []string{protocol.StatusSucceeded, protocol.StatusFailed} {
			if ack := report("a1", ex.AttemptID, status); !ack.Stale {
				t.Fatalf("duplicate %s ack = %+v; want stale", status, ack)
			}
		}
		if got, _ := st.GetExecution(ctx, ex.ID); got.State != store.ExecSucceeded || enq.count() != 1 {
			t.Fatalf("duplicate changed state to %s or enqueued %d", got.State, enq.count())
		}
		if _, err := d.Complete(ctx, "a1", protocol.StepComplete{AttemptID: ex.AttemptID, Status: "running"}); err == nil {
			t.Fatal("a non-terminal status was accepted")
		}
	})
}

func TestReaperMarksExpiredLeaseLost(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		start := time.Now()
		var mu sync.Mutex
		now := start
		clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
		d, enq := newBare(t, st, clock)
		ex := claimed(t, st, start)
		if err := d.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Stop(ctx) })

		time.Sleep(100 * time.Millisecond) // reaper ticks while the lease is valid
		if got, _ := st.GetExecution(ctx, ex.ID); got.State != store.ExecRunning {
			t.Fatalf("state = %s before expiry; want running", got.State)
		}
		mu.Lock()
		now = start.Add(2 * time.Minute)
		mu.Unlock()
		waitFor(t, func() bool { got, _ := st.GetExecution(ctx, ex.ID); return got.State == store.ExecLost })
		waitFor(t, func() bool { return enq.count() > 0 })
		if step, _ := st.GetStepInstance(ctx, ex.StepInstanceID); step.Status != store.StepFailed {
			t.Fatalf("step = %s; want failed", step.Status)
		}
		// A report for the lost attempt is stale.
		if ack, err := d.Complete(ctx, "a1", protocol.StepComplete{AttemptID: ex.AttemptID, Status: protocol.StatusSucceeded}); err != nil || !ack.Stale {
			t.Fatalf("late report = %+v, %v; want stale", ack, err)
		}
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
