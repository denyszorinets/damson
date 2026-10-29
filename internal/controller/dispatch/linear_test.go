package dispatch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/store"
)

func TestLinearRunEndToEnd(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := startCluster(t, st, twoStepSpec)
		tree := c.waitRun(c.trigger("one"), store.RunSucceeded)
		if len(tree.Steps) != 2 {
			t.Fatalf("steps = %d; want 2", len(tree.Steps))
		}
		for _, n := range tree.Steps {
			if n.Status != store.StepSucceeded || len(n.Executions) != 1 || n.Executions[0].State != store.ExecSucceeded {
				t.Fatalf("step %s = %s %+v; want one succeeded execution", n.Path, n.Status, n.Executions)
			}
		}
		ex := tree.Steps[0].Executions[0]
		if ex.AgentID != "agent-1" || ex.ControllerID != "ctl-1" {
			t.Fatalf("owner = %q/%q", ex.AgentID, ex.ControllerID)
		}
		waitLog(t, st, ex.ID, "said one")
	})
}

func TestFailedStepFailsRun(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		spec := `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"demo","with":{"word":"boom"}}]}}`
		c := startCluster(t, st, spec)
		tree := c.waitRun(c.trigger("boom"), store.RunFailed)
		if n := tree.Steps[0]; n.Status != store.StepFailed || n.Error == "" {
			t.Fatalf("step = %s %q; want failed with an error", n.Status, n.Error)
		}
	})
}

// waitLog waits until a persisted log chunk of the execution contains want.
func waitLog(t *testing.T, st store.Store, execID, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		chunks, err := st.ListLogChunks(context.Background(), execID, 1, 1<<30, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, ch := range chunks {
			data, err := st.GetLogData(context.Background(), ch.ObjectKey)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), want) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("log of %s never contained %q", execID, want)
}

// A step that outlives the lease TTL survives because heartbeats renew it.
func TestLeaseRenewedWhileRunning(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := startCluster(t, st, twoStepSpec, func(cfg *dispatch.Config) { cfg.LeaseTTL = 400 * time.Millisecond })
		tree := c.waitRun(c.trigger("slow"), store.RunSucceeded)
		if got := tree.Steps[0].Executions[0].State; got != store.ExecSucceeded {
			t.Fatalf("first execution = %s; want succeeded (not lost)", got)
		}
	})
}

// An execution no connected agent can run goes back to the queue instead of
// sticking in dispatched.
func TestIncapableAgentRequeues(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		spec := `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"missing","with":{}}]}}`
		c := startCluster(t, st, spec)
		id := c.triggerPlugin("missing", "one")
		time.Sleep(500 * time.Millisecond)
		tree, err := st.GetRunTree(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if ex := tree.Steps[0].Executions[0]; ex.State != store.ExecQueued || ex.AgentID != "" {
			t.Fatalf("execution = %s on %q; want queued and unowned", ex.State, ex.AgentID)
		}
	})
}
