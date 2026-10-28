package advance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/store"
)

var errRollback = errors.New("rollback on purpose")

func newTestAdvancer(t *testing.T, st store.Store, sweep time.Duration) *Advancer {
	t.Helper()
	eng, err := engine.New(expr.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{Store: st, Engine: eng, PollInterval: 50 * time.Millisecond, SweepInterval: sweep})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func startAdvancer(t *testing.T, a *Advancer) {
	t.Helper()
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
}

func seedJob(t *testing.T, st store.Store) store.JobVersion {
	t.Helper()
	_, jv, err := st.CreateJob(context.Background(), store.NewJob{
		ProjectID: store.DefaultProjectID, Name: "lin", Spec: json.RawMessage(linearSpec), Checksum: "sum",
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	return jv
}

// newRun creates a run the way the API trigger does: pending, with the first
// step instance and its queued execution.
func newRun(jv store.JobVersion) store.NewRun {
	return store.NewRun{
		JobID: jv.JobID, JobVersionID: jv.ID, Trigger: json.RawMessage(`{"type":"manual"}`), Options: json.RawMessage(`{}`),
		Steps: []store.NewStepInstance{{Path: "s1", Plugin: "shell", Input: json.RawMessage(`{"cmd":"one"}`),
			Execution: &store.NewExecution{Pool: DefaultPool, ScheduledAt: t0}}},
	}
}

func trigger(t *testing.T, st store.Store, a *Advancer, jv store.JobVersion) string {
	t.Helper()
	var id string
	err := st.WithTx(context.Background(), func(tx store.Tx) error {
		tree, err := tx.CreateRun(context.Background(), newRun(jv), t0)
		if err != nil {
			return err
		}
		id = tree.ID
		return a.Enqueue(context.Background(), tx, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// countJobs counts run.advance jobs in River's table that have not finished.
func countJobs(t *testing.T, st store.Store) int {
	t.Helper()
	const q = "SELECT count(*) FROM river_job WHERE kind = 'run.advance' AND state IN ('available', 'pending', 'running', 'retryable', 'scheduled')"
	var n int
	var err error
	switch s := st.(type) {
	case interface{ Pool() *pgxpool.Pool }:
		err = s.Pool().QueryRow(context.Background(), q).Scan(&n)
	case interface{ QueueDB() *sql.DB }:
		err = s.QueueDB().QueryRowContext(context.Background(), q).Scan(&n)
	}
	if err != nil {
		t.Fatalf("counting jobs: %v", err)
	}
	return n
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func loadTree(t *testing.T, st store.Store, id string) store.RunTree {
	t.Helper()
	tree, err := st.GetRunTree(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// finishStep plays the agent and the completion handler of slice 53c: it
// claims the queued execution of pool, completes it and its step, and wakes
// the run in one transaction (TouchRun + Enqueue).
func finishStep(t *testing.T, st store.Store, a *Advancer, runID, pool string, state store.ExecutionState) {
	t.Helper()
	ctx := context.Background()
	claimed, err := st.ClaimExecutions(ctx, store.ClaimParams{
		AgentID: "agent", ControllerID: "ctl", Pools: []string{pool}, Limit: 1, LeaseTTL: time.Minute,
	}, time.Now())
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	ex := claimed[0]
	if _, err := st.CompleteExecution(ctx, store.Completion{ExecutionID: ex.ID, AttemptID: ex.AttemptID, State: state, Outputs: json.RawMessage(`{}`)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	step := store.StepSucceeded
	if state != store.ExecSucceeded {
		step = store.StepFailed
	}
	err = st.WithTx(ctx, func(tx store.Tx) error {
		if _, err := tx.TouchRun(ctx, runID); err != nil {
			return err
		}
		return a.Enqueue(ctx, tx, runID)
	})
	if err != nil {
		t.Fatal(err)
	}
	// The step flip is separate here only because the test plays 53c by hand;
	// the enqueue above is already in the queue and a re-advance after the
	// flip is exactly what happens when completion order interleaves.
	if _, err := st.UpdateStepInstance(ctx, store.StepUpdate{ID: ex.StepInstanceID, ExpectedStatus: store.StepPending, Status: step}, time.Now()); err != nil {
		t.Fatal(err)
	}
	err = st.WithTx(ctx, func(tx store.Tx) error {
		if _, err := tx.TouchRun(ctx, runID); err != nil {
			return err
		}
		return a.Enqueue(ctx, tx, runID)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunToCompletion(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		a := newTestAdvancer(t, st, -1)
		startAdvancer(t, a)
		jv := seedJob(t, st)
		id := trigger(t, st, a, jv)
		ch, release := a.Hub().Subscribe(context.Background(), id)
		defer release()

		eventually(t, "run to start", func() bool { return loadTree(t, st, id).Status == store.RunRunning })
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("subscriber was not notified of the committed advance")
		}
		tree := loadTree(t, st, id)
		if len(tree.Steps) != 1 || tree.Steps[0].Path != "s1" || len(tree.Steps[0].Executions) != 1 {
			t.Fatalf("trigger rows were duplicated or lost: %+v", tree.Steps)
		}

		finishStep(t, st, a, id, DefaultPool, store.ExecSucceeded)
		eventually(t, "step two to be scheduled", func() bool { return len(loadTree(t, st, id).Steps) == 2 })
		s2 := loadTree(t, st, id).Steps[1]
		if s2.Path != "s2" || len(s2.Executions) != 1 || s2.Executions[0].Pool != "gpu" || s2.Executions[0].State != store.ExecQueued {
			t.Fatalf("step two = %+v", s2)
		}

		finishStep(t, st, a, id, "gpu", store.ExecSucceeded)
		eventually(t, "run to succeed", func() bool { return loadTree(t, st, id).Status == store.RunSucceeded })
		if tree := loadTree(t, st, id); tree.FinishedAt == nil || tree.StartedAt == nil {
			t.Fatalf("timestamps missing: %+v", tree.Run)
		}
		eventually(t, "queue to drain", func() bool { return countJobs(t, st) == 0 })
	})
}

func TestFailedStepFailsRun(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		a := newTestAdvancer(t, st, -1)
		startAdvancer(t, a)
		id := trigger(t, st, a, seedJob(t, st))
		eventually(t, "run to start", func() bool { return loadTree(t, st, id).Status == store.RunRunning })
		finishStep(t, st, a, id, DefaultPool, store.ExecFailed)
		eventually(t, "run to fail", func() bool { return loadTree(t, st, id).Status == store.RunFailed })
		if s2 := loadTree(t, st, id).Steps[1]; s2.Status != store.StepSkipped || len(s2.Executions) != 0 {
			t.Fatalf("step two = %+v", s2)
		}
	})
}

func TestCancelQueuedRun(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		a := newTestAdvancer(t, st, -1)
		startAdvancer(t, a)
		id := trigger(t, st, a, seedJob(t, st))
		eventually(t, "run to start", func() bool { return loadTree(t, st, id).Status == store.RunRunning })
		ctx := context.Background()
		err := st.WithTx(ctx, func(tx store.Tx) error {
			if _, err := tx.RequestCancel(ctx, id, time.Now()); err != nil {
				return err
			}
			return a.Enqueue(ctx, tx, id)
		})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "run to be cancelled", func() bool { return loadTree(t, st, id).Status == store.RunCanceled })
		tree := loadTree(t, st, id)
		if tree.Steps[0].Status != store.StepCanceled || tree.Steps[0].Executions[0].State != store.ExecCanceled {
			t.Fatalf("step one = %+v", tree.Steps[0])
		}
	})
}

func TestEnqueueUniqueness(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		a := newTestAdvancer(t, st, -1) // not started: insert only
		ctx := context.Background()
		id := trigger(t, st, a, seedJob(t, st))
		for range 2 {
			err := st.WithTx(ctx, func(tx store.Tx) error { return a.Enqueue(ctx, tx, id) })
			if err != nil {
				t.Fatal(err)
			}
		}
		if n := countJobs(t, st); n != 1 {
			t.Fatalf("jobs after three enqueues at one version = %d, want 1", n)
		}
		// A change bumps the version, so its enqueue is kept even though the
		// first job is still waiting (or, in production, running).
		err := st.WithTx(ctx, func(tx store.Tx) error {
			if _, err := tx.TouchRun(ctx, id); err != nil {
				return err
			}
			return a.Enqueue(ctx, tx, id)
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := countJobs(t, st); n != 2 {
			t.Fatalf("jobs after a version bump = %d, want 2", n)
		}
	})
}

func TestEnqueueRollsBackWithTransaction(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		a := newTestAdvancer(t, st, -1)
		jv := seedJob(t, st)
		var id string
		err := st.WithTx(context.Background(), func(tx store.Tx) error {
			tree, err := tx.CreateRun(context.Background(), newRun(jv), t0)
			if err != nil {
				return err
			}
			id = tree.ID
			if err := a.Enqueue(context.Background(), tx, id); err != nil {
				return err
			}
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("WithTx = %v", err)
		}
		if n := countJobs(t, st); n != 0 {
			t.Fatalf("rolled back transaction left %d jobs", n)
		}
		if _, err := st.GetRun(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("rolled back run is visible: %v", err)
		}
	})
}

func TestConcurrentAdvanceAppliesOnce(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		a := newTestAdvancer(t, st, -1)
		id := trigger(t, st, a, seedJob(t, st))
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- a.Advance(context.Background(), id)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("Advance: %v", err)
			}
		}
		tree := loadTree(t, st, id)
		if tree.Status != store.RunRunning || tree.Version != 2 || len(tree.Steps) != 1 || len(tree.Steps[0].Executions) != 1 {
			t.Fatalf("after 8 concurrent advances: version %d, status %s, steps %+v", tree.Version, tree.Status, tree.Steps)
		}
		// Advancing an unchanged run writes nothing.
		if err := a.Advance(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		if v := loadTree(t, st, id).Version; v != 2 {
			t.Fatalf("idempotent advance bumped version to %d", v)
		}
	})
}

func TestSweepEnqueuesLostRuns(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		a := newTestAdvancer(t, st, -1)
		jv := seedJob(t, st)
		ctx := context.Background()
		// A run inserted without any enqueue: the pre-River / crash window.
		tree, err := st.CreateRun(ctx, newRun(jv), t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if err := a.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if n := countJobs(t, st); n != 1 {
			t.Fatalf("jobs after two sweeps = %d, want 1 (uniqueness)", n)
		}
		startAdvancer(t, a)
		eventually(t, "swept run to start", func() bool { return loadTree(t, st, tree.ID).Status == store.RunRunning })
	})
}

// A step marked running whose execution went back to queued (lease lost,
// requeue) must still be cancellable: the cancel moves the step from running,
// not only from pending, or the whole transaction would fail forever.
func TestCancelRunWithRunningStepAndQueuedExecution(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		a := newTestAdvancer(t, st, -1)
		startAdvancer(t, a)
		id := trigger(t, st, a, seedJob(t, st))
		eventually(t, "run to start", func() bool { return loadTree(t, st, id).Status == store.RunRunning })
		ctx := context.Background()
		tree := loadTree(t, st, id)
		if _, err := st.UpdateStepInstance(ctx, store.StepUpdate{ID: tree.Steps[0].ID, ExpectedStatus: store.StepPending, Status: store.StepRunning}, time.Now()); err != nil {
			t.Fatal(err)
		}
		err := st.WithTx(ctx, func(tx store.Tx) error {
			if _, err := tx.RequestCancel(ctx, id, time.Now()); err != nil {
				return err
			}
			return a.Enqueue(ctx, tx, id)
		})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "run to be cancelled", func() bool { return loadTree(t, st, id).Status == store.RunCanceled })
		if s := loadTree(t, st, id).Steps[0]; s.Status != store.StepCanceled || s.Executions[0].State != store.ExecCanceled {
			t.Fatalf("step = %+v", s)
		}
	})
}

// A run the engine can never advance (a spec it rejects) is failed instead of
// lingering as running: the sweep would otherwise re-queue it forever and a
// cancel would hit the same error.
func TestPermanentEngineErrorFailsRun(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		a := newTestAdvancer(t, st, -1)
		startAdvancer(t, a)
		ctx := context.Background()
		_, ver, err := st.CreateJob(ctx, store.NewJob{
			ProjectID: store.DefaultProjectID, Name: "broken", Spec: json.RawMessage(`{"not":"a job"}`), Checksum: "broken",
		}, t0)
		if err != nil {
			t.Fatal(err)
		}
		id := trigger(t, st, a, ver)
		eventually(t, "run to be failed", func() bool { return loadTree(t, st, id).Status == store.RunFailed })
		eventually(t, "queue to drain", func() bool { return countJobs(t, st) == 0 })
		if err := a.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if n := countJobs(t, st); n != 0 {
			t.Fatalf("%d run.advance jobs after the sweep; a failed run must not be re-queued", n)
		}
	})
}
