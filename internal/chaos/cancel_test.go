//go:build chaos

package chaos

import (
	"context"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// cancelVia cancels the run through controller idx the way the API does.
func (c *Cluster) cancelVia(idx int, runID string) {
	c.t.Helper()
	ctl := c.ctls[idx]
	ctx := context.Background()
	err := ctl.st.WithTx(ctx, func(tx store.Tx) error {
		if _, err := tx.RequestCancel(ctx, runID, time.Now()); err != nil {
			return err
		}
		return ctl.node.Advancer().Enqueue(ctx, tx, runID)
	})
	if err != nil {
		c.t.Fatal(err)
	}
}

// waitCancelled waits for the run to end and checks that the held step was
// stopped under its original attempt, never lost, and that nothing else ran.
func (c *Cluster) waitCancelled(runID, attemptID string, gp *gatePlugin) {
	c.t.Helper()
	var tree store.RunTree
	eventually(c.t, "run to finish", func() bool {
		var err error
		if tree, err = c.read.GetRunTree(context.Background(), runID); err != nil {
			c.t.Fatal(err)
		}
		return tree.Status.Terminal()
	})
	if tree.Status != store.RunCanceled {
		c.t.Fatalf("run status = %s; want canceled (steps %+v)", tree.Status, tree.Steps)
	}
	s1 := tree.Steps[0]
	if len(s1.Executions) != 1 {
		c.t.Fatalf("s1 has %d executions; want 1: %+v", len(s1.Executions), s1.Executions)
	}
	ex := s1.Executions[0]
	if ex.State != store.ExecCanceled || ex.AttemptID != attemptID || ex.CancelRequestedAt == nil {
		c.t.Fatalf("s1 execution = %+v; want canceled, flagged, attempt %s (not lost)", ex, attemptID)
	}
	if n := gp.callCount("hold"); n != 1 || !gp.wasCancelled("hold") {
		c.t.Fatalf("plugin ran %d times, cancelled=%v; want once and cancelled", n, gp.wasCancelled("hold"))
	}
	if n := gp.callCount("two"); n != 0 {
		c.t.Fatalf("step two ran %d times after the cancel", n)
	}
}

// K1 (#83): the cancel is requested through a controller that does not own the
// running step. The owner finds the flag and stops the plugin.
func TestCancelThroughAnotherController(t *testing.T) {
	c := newCluster(t, 2)
	gp := newGatePlugin()
	c.addAgent("ag1", 0, gp)
	runID := c.triggerWord(0, "gate", "hold")
	first := c.runningOn(runID, "ctl-A")

	c.cancelVia(1, runID)
	c.waitCancelled(runID, first.AttemptID, gp)
}

// K2 (#83): the agent is cut off from its controller when the cancel is
// requested. The flag waits in the database; the controller that adopts the
// agent sends the cancel, within one lease.
func TestCancelWhileAgentMoves(t *testing.T) {
	c := newCluster(t, 2)
	gp := newGatePlugin()
	c.addAgent("ag1", 0, gp)
	runID := c.triggerWord(0, "gate", "hold")
	first := c.runningOn(runID, "ctl-A")

	c.lb.route("ag1", "")
	c.lb.sever("ag1")
	c.cancelVia(1, runID)
	eventually(t, "the cancel to be flagged", func() bool {
		s, ok := c.step(runID, "s1")
		return ok && len(s.Executions) > 0 && s.Executions[0].CancelRequestedAt != nil
	})
	if s, _ := c.step(runID, "s1"); s.Executions[0].State != store.ExecRunning {
		t.Fatalf("execution = %s while the agent is away; want it still running", s.Executions[0].State)
	}
	c.lb.route("ag1", "ctl-B")

	c.waitCancelled(runID, first.AttemptID, gp)
}
