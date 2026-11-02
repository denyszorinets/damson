package dispatch_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
)

// requestCancel cancels the run the way the API does: the request and the
// wake-up in one transaction.
func (c *cluster) requestCancel(id string) {
	c.t.Helper()
	ctx := context.Background()
	err := c.st.WithTx(ctx, func(tx store.Tx) error {
		if _, err := tx.RequestCancel(ctx, id, time.Now()); err != nil {
			return err
		}
		return c.adv.Enqueue(ctx, tx, id)
	})
	if err != nil {
		c.t.Fatal(err)
	}
}

func (c *cluster) waitExecution(id string, want store.ExecutionState) store.Execution {
	c.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		tree, err := c.st.GetRunTree(context.Background(), id)
		if err != nil {
			c.t.Fatal(err)
		}
		if len(tree.Steps) > 0 && len(tree.Steps[0].Executions) > 0 {
			if ex := tree.Steps[0].Executions[0]; ex.State == want {
				return ex
			}
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("timed out waiting for the first execution to be %s (steps %+v)", want, tree.Steps)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A cancel reaches a step that is already running: the owning controller sends
// step.cancel, the plugin stops, and the run ends cancelled without ever
// starting the second step.
func TestCancelRunningStep(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := startCluster(t, st, twoStepSpec)
		id := c.trigger("hold")
		first := c.waitExecution(id, store.ExecRunning)

		c.requestCancel(id)
		tree := c.waitRun(id, store.RunCanceled)

		s1 := tree.Steps[0]
		if s1.Status != store.StepCanceled || len(s1.Executions) != 1 {
			t.Fatalf("s1 = %s with %d executions; want canceled with 1", s1.Status, len(s1.Executions))
		}
		ex := s1.Executions[0]
		if ex.State != store.ExecCanceled || ex.AttemptID != first.AttemptID || ex.CancelRequestedAt == nil {
			t.Fatalf("execution = %+v; want canceled, flagged, same attempt %s", ex, first.AttemptID)
		}
		for _, n := range tree.Steps[1:] {
			if len(n.Executions) != 0 {
				t.Fatalf("step %s has executions %+v after the cancel", n.Path, n.Executions)
			}
		}
	})
}

// gatedResolver holds the first resolution (the one that precedes the first
// step.execute) until the test releases it, then fails it, so the execution
// goes back to the dispatcher's requeue path while the test flags it.
type gatedResolver struct {
	inner   dispatch.Resolver
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedResolver) Resolve(ref string, f pluginreg.Filter) (pluginreg.Resolution, error) {
	first := false
	g.once.Do(func() {
		first = true
		close(g.entered)
		<-g.release
	})
	if first {
		return pluginreg.Resolution{}, errors.New("gated: resolution failed on purpose")
	}
	return g.inner.Resolve(ref, f)
}

// A flagged attempt that was claimed but never delivered is not requeued and
// run again: it ends canceled and so does the run.
func TestFlaggedUndeliveredExecutionIsCanceledNotRequeued(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		g := &gatedResolver{entered: make(chan struct{}), release: make(chan struct{})}
		c := startCluster(t, st, twoStepSpec, func(cfg *dispatch.Config) {
			g.inner = cfg.Plugins
			cfg.Plugins = g
		})
		id := c.trigger("one")
		select {
		case <-g.entered:
		case <-time.After(20 * time.Second):
			t.Fatal("the dispatcher never resolved the plugin")
		}
		first := c.waitExecution(id, store.ExecDispatched)

		ctx := context.Background()
		err := st.WithTx(ctx, func(tx store.Tx) error {
			run, err := tx.RequestCancel(ctx, id, time.Now())
			if err != nil {
				return err
			}
			// What run.advance does for the claimed execution.
			_, err = tx.ApplyAdvance(ctx, id, run.Version, store.Advance{Cancel: []string{first.ID}}, time.Now())
			if err != nil {
				return err
			}
			return c.adv.Enqueue(ctx, tx, id)
		})
		if err != nil {
			t.Fatal(err)
		}
		close(g.release)

		tree := c.waitRun(id, store.RunCanceled)
		ex := tree.Steps[0].Executions[0]
		if len(tree.Steps[0].Executions) != 1 || ex.State != store.ExecCanceled || ex.AttemptID != first.AttemptID {
			t.Fatalf("executions = %+v; want the original attempt canceled and no other", tree.Steps[0].Executions)
		}
	})
}
