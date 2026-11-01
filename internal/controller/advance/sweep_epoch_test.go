package advance

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/store"
)

// An event job keeps the JSON, and so the unique key, it had before the sweep
// epoch existed: jobs queued at upgrade still collapse with new ones.
func TestEventArgsHaveNoSweepKey(t *testing.T) {
	b, err := json.Marshal(Args{RunID: "r1", Version: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"run_id":"r1","version":3}`; got != want {
		t.Fatalf("args JSON = %s; want %s", got, want)
	}
}

// strandRunning marks every queued run.advance job running, the footprint of
// a controller that died while working on it.
func strandRunning(t *testing.T, st store.Store) {
	t.Helper()
	const q = "UPDATE river_job SET state = 'running', attempt = 1 WHERE kind = 'run.advance'"
	var err error
	switch s := st.(type) {
	case interface{ Pool() *pgxpool.Pool }:
		_, err = s.Pool().Exec(context.Background(), q)
	case interface{ QueueDB() *sql.DB }:
		_, err = s.QueueDB().ExecContext(context.Background(), q)
	}
	if err != nil {
		t.Fatalf("stranding jobs: %v", err)
	}
}

// A job a dead controller left running holds the unique key of its (run,
// version); the run version does not move, so without the sweep epoch the
// sweep's job would collapse into it and the run would wait for River's
// rescuer. The sweep must insert a job of its own, once per epoch.
func TestSweepBypassesJobStrandedRunning(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		eng, err := engine.New(expr.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
		a, err := New(Config{Store: st, Engine: eng, PollInterval: 50 * time.Millisecond, SweepInterval: time.Minute, Now: func() time.Time { return at }})
		if err != nil {
			t.Fatal(err)
		}
		id := trigger(t, st, a, seedJob(t, st)) // not started: the job stays queued
		strandRunning(t, st)
		ctx := context.Background()

		if err := a.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if n := countJobs(t, st); n != 2 {
			t.Fatalf("jobs after the sweep = %d; want 2 (the stranded one and the sweep's)", n)
		}
		if err := a.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if n := countJobs(t, st); n != 2 {
			t.Fatalf("jobs after a second sweep in the same epoch = %d; want 2", n)
		}

		startAdvancer(t, a)
		eventually(t, "the sweep's job to advance the run", func() bool { return loadTree(t, st, id).Status == store.RunRunning })
	})
}

// The sweep runs once at start: a controller restarted after a crash must not
// wait a whole interval for the runs the crash stranded.
func TestSweepRunsAtStart(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		a := newTestAdvancer(t, st, time.Hour)
		tree, err := st.CreateRun(context.Background(), newRun(seedJob(t, st)), t0) // no enqueue
		if err != nil {
			t.Fatal(err)
		}
		startAdvancer(t, a)
		eventually(t, "the start sweep to advance the run", func() bool { return loadTree(t, st, tree.ID).Status == store.RunRunning })
	})
}
