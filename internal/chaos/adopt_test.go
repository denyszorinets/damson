//go:build chaos

package chaos

import (
	"context"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// runningOn waits until step s1 of the run has a running execution owned by
// controller ctl and returns it.
func (c *Cluster) runningOn(runID, ctl string) store.Execution {
	c.t.Helper()
	var ex store.Execution
	eventually(c.t, "s1 running on "+ctl, func() bool {
		s, ok := c.step(runID, "s1")
		if !ok || len(s.Executions) == 0 {
			return false
		}
		ex = s.Executions[len(s.Executions)-1]
		return ex.State == store.ExecRunning && ex.ControllerID == ctl
	})
	return ex
}

// waitSucceeded waits for the run to end and requires success.
func (c *Cluster) waitSucceeded(runID string) store.RunTree {
	c.t.Helper()
	var tree store.RunTree
	eventually(c.t, "run to finish", func() bool {
		var err error
		if tree, err = c.ctls[0].st.GetRunTree(context.Background(), runID); err != nil {
			c.t.Fatal(err)
		}
		return tree.Status.Terminal()
	})
	if tree.Status != store.RunSucceeded {
		c.t.Fatalf("run status = %s; want succeeded (steps %+v)", tree.Status, tree.Steps)
	}
	return tree
}

// assertExactlyOnce checks the end state: every step has exactly one
// execution (no retry, none lost), s1 kept its original attempt, the plugin
// ran once per step and was never cancelled, and a settle period changes
// nothing (a redelivered result was discarded as stale).
func assertExactlyOnce(t *testing.T, c *Cluster, runID, attemptID string, gp *gatePlugin) {
	t.Helper()
	tree := c.waitSucceeded(runID)
	if len(tree.Steps) != 2 {
		t.Fatalf("steps = %d; want 2", len(tree.Steps))
	}
	for _, s := range tree.Steps {
		if len(s.Executions) != 1 {
			t.Fatalf("step %s has %d executions; want exactly 1: %+v", s.Path, len(s.Executions), s.Executions)
		}
		if ex := s.Executions[0]; ex.State != store.ExecSucceeded || ex.FinishedAt == nil {
			t.Fatalf("step %s execution = %+v; want succeeded and finished", s.Path, ex)
		}
	}
	if got := tree.Steps[0].Executions[0].AttemptID; got != attemptID {
		t.Fatalf("s1 attempt = %s; want the original %s", got, attemptID)
	}
	for _, w := range []string{"one", "two"} {
		if n := gp.callCount(w); n != 1 {
			t.Fatalf("plugin ran %d times for %q; want 1", n, w)
		}
		if gp.wasCancelled(w) {
			t.Fatalf("plugin for %q was cancelled", w)
		}
	}
	finished := tree.Steps[0].Executions[0].FinishedAt
	time.Sleep(500 * time.Millisecond) // several redelivery rounds
	after, err := c.ctls[0].st.GetRunTree(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Steps[0].Executions[0].FinishedAt; !got.Equal(*finished) || after.Status != store.RunSucceeded {
		t.Fatalf("state changed after settling: finished_at %v -> %v, status %s", finished, got, after.Status)
	}
}

// A1 (#60): the agent reconnects to another controller while the step runs.
// The new controller adopts the attempt, so the reaper never marks it lost
// even though the gate stays closed for longer than a lease.
func TestAgentMovesToAnotherControllerMidStep(t *testing.T) {
	c := newCluster(t, 2)
	gp := newGatePlugin()
	c.addAgent("ag1", 0, gp)
	runID := c.trigger(0)
	first := c.runningOn(runID, "ctl-A")

	c.lb.route("ag1", "ctl-B")
	c.lb.sever("ag1")
	adopted := c.runningOn(runID, "ctl-B")
	if adopted.AttemptID != first.AttemptID {
		t.Fatalf("attempt changed on adoption: %s -> %s", first.AttemptID, adopted.AttemptID)
	}

	// Hold the step for more than two leases: without adoption the old
	// controller's renewals stop and the reaper marks it lost.
	end := time.Now().Add(2 * leaseTTL)
	for time.Now().Before(end) {
		s, _ := c.step(runID, "s1")
		if ex := s.Executions[len(s.Executions)-1]; ex.State != store.ExecRunning || ex.AttemptID != first.AttemptID {
			t.Fatalf("execution = %+v while the step is held; want the original attempt running", ex)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if now := c.runningOn(runID, "ctl-B"); now.LeaseExpiresAt == nil || adopted.LeaseExpiresAt == nil || !now.LeaseExpiresAt.After(*adopted.LeaseExpiresAt) {
		t.Fatalf("lease did not advance under the new controller: %v -> %v", adopted.LeaseExpiresAt, now.LeaseExpiresAt)
	}

	gp.open()
	assertExactlyOnce(t, c, runID, first.AttemptID, gp)
}

// A2 (#60): the step finishes while the agent is cut off. After it reaches
// another controller the held result is redelivered and applied once.
func TestResultFinishedWhileDisconnectedIsAppliedOnce(t *testing.T) {
	c := newCluster(t, 2)
	gp := newGatePlugin()
	c.addAgent("ag1", 0, gp)
	runID := c.trigger(0)
	first := c.runningOn(runID, "ctl-A")

	c.lb.route("ag1", "")
	c.lb.sever("ag1")
	gp.open()
	eventually(t, "plugin to return while disconnected", func() bool { return gp.returned("one") })
	c.lb.route("ag1", "ctl-B")

	assertExactlyOnce(t, c, runID, first.AttemptID, gp)
}
