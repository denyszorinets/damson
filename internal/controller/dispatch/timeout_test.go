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

const jobTimeoutSpec = `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"timeout":"1500ms","steps":[{"id":"s1","plugin":"demo","with":{"word":"hold"}},{"id":"s2","plugin":"demo","with":{"word":"two"}},
{"id":"cleanup","plugin":"demo","when":"always()","with":{"word":"clean"}}]}}`

// The job-level timeout stops the running step through step.cancel, skips the
// pending one as cancelled, still runs the always() cleanup step, and fails
// the run, with the timeout recorded as the cancel reason.
func TestJobTimeoutStopsRunAndRunsCleanup(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := startCluster(t, st, jobTimeoutSpec)
		id := c.trigger("hold")

		tree := c.waitRun(id, store.RunFailed)
		if tree.CancelReason != store.CancelReasonTimeout || tree.CancelRequestedAt == nil {
			t.Fatalf("cancel = %v reason %q; want the job timeout recorded", tree.CancelRequestedAt, tree.CancelReason)
		}
		byPath := map[string]store.StepNode{}
		for _, n := range tree.Steps {
			byPath[n.Path] = n
		}
		s1 := byPath["s1"]
		if s1.Status != store.StepCanceled || len(s1.Executions) != 1 || s1.Executions[0].State != store.ExecCanceled {
			t.Fatalf("s1 = %s %+v; want canceled with one canceled execution", s1.Status, s1.Executions)
		}
		if s2 := byPath["s2"]; s2.Status != store.StepCanceled || s2.Error != "job timed out" || len(s2.Executions) != 0 {
			t.Fatalf("s2 = %s %q with %d executions; want canceled as job timed out, never run", s2.Status, s2.Error, len(s2.Executions))
		}
		clean := byPath["cleanup"]
		if clean.Status != store.StepSucceeded || len(clean.Executions) != 1 {
			t.Fatalf("cleanup = %s with %d executions; want it to run and succeed", clean.Status, len(clean.Executions))
		}
	})
}
