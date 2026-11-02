package dispatch_test

import (
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// A step with a timeout is stopped by the agent's deadline: the attempt ends
// timed_out, the step fails with error kind timeout, and the run fails. The
// controller only relays the deadline in step.execute and applies the report.
func TestStepTimeoutEndsTimedOut(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := startCluster(t, st, twoStepSpec)
		c.firstTimeout = 300 * time.Millisecond
		id := c.trigger("hold")

		tree := c.waitRun(id, store.RunFailed)
		s1 := tree.Steps[0]
		if s1.Status != store.StepFailed || len(s1.Executions) != 1 {
			t.Fatalf("s1 = %s with %d executions; want failed with 1", s1.Status, len(s1.Executions))
		}
		ex := s1.Executions[0]
		if ex.State != store.ExecTimedOut || ex.Timeout != 300*time.Millisecond {
			t.Fatalf("execution = %+v; want timed_out with the 300ms timeout stored", ex)
		}
		if kind, msg := store.SplitStepError(s1.Error); kind != store.ErrKindTimeout || msg == "" {
			t.Fatalf("step error = %q; want kind timeout with a message", s1.Error)
		}
		if got := store.ErrorKindOf(ex); got != store.ErrKindTimeout {
			t.Fatalf("execution error kind = %q; want timeout", got)
		}
		if len(tree.Steps) > 1 && len(tree.Steps[1].Executions) != 0 {
			t.Fatalf("second step ran after the timeout: %+v", tree.Steps[1])
		}
	})
}
