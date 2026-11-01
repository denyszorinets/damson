//go:build chaos

package chaos

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres"
)

// gatedStore blocks the run.advance that follows the end of s1 of the armed
// run, standing in for a controller that is busy in that advance when it dies.
// It reads the run first, so it holds no connection while it blocks.
type gatedStore struct {
	*postgres.Store

	armed   atomic.Value // run id
	entered chan struct{}
	once    sync.Once
}

func newGatedStore(pg *postgres.Store) *gatedStore {
	return &gatedStore{Store: pg, entered: make(chan struct{})}
}

func (g *gatedStore) GetRunTree(ctx context.Context, id string) (store.RunTree, error) {
	tree, err := g.Store.GetRunTree(ctx, id)
	if err != nil || id != g.armed.Load() {
		return tree, err
	}
	for _, s := range tree.Steps {
		if s.Path == "s1" && s.Status == store.StepSucceeded {
			g.once.Do(func() { close(g.entered) })
			<-ctx.Done()
			return store.RunTree{}, ctx.Err()
		}
	}
	return tree, nil
}

// advanceJobState is the state of a River run.advance job.
func (c *Cluster) advanceJobState(id int64) string {
	c.t.Helper()
	var state string
	if err := c.read.Pool().QueryRow(context.Background(), "SELECT state FROM river_job WHERE id = $1", id).Scan(&state); err != nil {
		c.t.Fatal(err)
	}
	return state
}

// B3 (#77): a controller dies in the middle of the run.advance that follows
// s1, leaving its River job running. The run version does not change, so
// without the sweep epoch the sweep of the surviving controller would collapse
// into that job and the run would wait for River's rescuer (an hour by
// default). The run must finish within the sweep interval instead, with the
// stranded job still running to prove the rescuer was not involved.
func TestRunAdvancesAfterControllerDiesMidAdvance(t *testing.T) {
	c := newEmptyCluster(t)
	var gs *gatedStore
	noSweep := timing
	noSweep.AdvanceSweep = -1 // A never sweeps, B does
	c.addController(noSweep, func(pg *postgres.Store) store.Store { gs = newGatedStore(pg); return gs })

	gp := newGatePlugin()
	c.addAgent("ag1", 0, gp)
	runID := c.trigger(0)
	first := c.runningOn(runID, "ctl-A")
	gs.armed.Store(runID)
	gp.open()
	select {
	case <-gs.entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for A to start the advance after s1")
	}

	var stranded int64
	rows, err := c.read.Pool().Query(context.Background(),
		"SELECT id FROM river_job WHERE kind = 'run.advance' AND args->>'run_id' = $1 AND state = 'running'", runID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		if stranded != 0 {
			t.Fatal("more than one running advance job for the run")
		}
		if err := rows.Scan(&stranded); err != nil {
			t.Fatal(err)
		}
	}
	rows.Close()
	if stranded == 0 {
		t.Fatal("no running advance job for the run")
	}

	// A's database connections go away and its agent is cut off: every write
	// from A fails from now on and the job never completes.
	c.lb.route("ag1", "")
	c.lb.sever("ag1")
	if err := c.ctls[0].st.Close(); err != nil {
		t.Fatal(err)
	}

	c.addController(timing, nil)
	c.lb.route("ag1", "ctl-B")

	assertExactlyOnce(t, c, runID, first.AttemptID, gp)
	if state := c.advanceJobState(stranded); state != "running" {
		t.Fatalf("stranded job %d is %s; want it still running (the sweep recovered the run, not the rescuer)", stranded, state)
	}
}
