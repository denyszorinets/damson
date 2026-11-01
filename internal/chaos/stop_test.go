//go:build chaos

package chaos

import (
	"context"
	"testing"
)

// instanceIDs lists the registered controller instances.
func (c *Cluster) instanceIDs() map[string]bool {
	c.t.Helper()
	list, err := c.ctls[1].st.ListControllerInstances(context.Background())
	if err != nil {
		c.t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, in := range list {
		ids[in.ID] = true
	}
	return ids
}

// B2 (#60): a controller stops gracefully while its agent runs a step
// (ARCHITECTURE 8.5). It deregisters, the agent reconnects to the other
// controller on its own, and the step finishes exactly once on the original
// attempt.
func TestGracefulControllerStopMovesAgentsAndDeregisters(t *testing.T) {
	c := newCluster(t, 2)
	gp := newGatePlugin()
	c.addAgent("ag1", 0, gp)
	runID := c.trigger(0)
	first := c.runningOn(runID, "ctl-A")
	if ids := c.instanceIDs(); !ids["ctl-A"] || !ids["ctl-B"] {
		t.Fatalf("registered controllers = %v; want ctl-A and ctl-B", ids)
	}

	// New connections go to the survivor; Stop sends agent.reconnect.
	c.lb.route("ag1", "ctl-B")
	if err := c.ctls[0].stop(); err != nil {
		t.Fatalf("graceful stop: %v", err)
	}
	if ids := c.instanceIDs(); ids["ctl-A"] || !ids["ctl-B"] {
		t.Fatalf("registered controllers after stop = %v; want only ctl-B", ids)
	}

	adopted := c.runningOn(runID, "ctl-B")
	if adopted.AttemptID != first.AttemptID {
		t.Fatalf("attempt changed on adoption: %s -> %s", first.AttemptID, adopted.AttemptID)
	}
	gp.open()
	assertExactlyOnce(t, c, runID, first.AttemptID, gp)
}
