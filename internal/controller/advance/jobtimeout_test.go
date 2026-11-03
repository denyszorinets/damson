package advance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

const timeoutSpec = `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"timeout"},
"spec":{"timeout":"1s","steps":[{"id":"s1","plugin":"shell","with":{"cmd":"one"}},{"id":"s2","plugin":"shell","with":{"cmd":"two"}},
{"id":"cleanup","plugin":"shell","when":"always()","with":{"cmd":"clean"}}]}}`

// The job-level timeout fires from the wake job queued when the run starts,
// not from the sweep (disabled here). The queued step is cancelled, the pending
// one is cancelled as "job timed out", the always() step is still created, and
// once it ends the run fails with the timeout recorded as the cancel reason.
func TestJobTimeoutWakesRunWithoutSweep(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		a := newTestAdvancer(t, st, -1)
		startAdvancer(t, a)
		ctx := context.Background()
		_, jv, err := st.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "timeout", Spec: json.RawMessage(timeoutSpec), Checksum: "t"}, t0)
		if err != nil {
			t.Fatal(err)
		}
		id := trigger(t, st, a, jv)
		eventually(t, "the run to start", func() bool { return loadTree(t, st, id).Status == store.RunRunning })
		started := time.Now()

		eventually(t, "the timeout to be recorded", func() bool { return loadTree(t, st, id).CancelRequestedAt != nil })
		// The deadline is 1s after the start; River moves scheduled jobs to
		// available every 5s, so the wake fires up to ~5s later. The bound only
		// proves it was the wake job (the sweep is disabled), not the sweep.
		if waited := time.Since(started); waited > 30*time.Second {
			t.Fatalf("timeout took %v; the wake job should fire within a scheduler cycle of the deadline", waited)
		}
		tree := loadTree(t, st, id)
		if tree.CancelReason != store.CancelReasonTimeout {
			t.Fatalf("cancel reason = %q; want %q", tree.CancelReason, store.CancelReasonTimeout)
		}
		// s1 never ran: it is cancelled; s2 is skipped as cancelled with the
		// timeout as the reason; the always() step is created and queued.
		eventually(t, "the cleanup step to be created", func() bool {
			for _, n := range loadTree(t, st, id).Steps {
				if n.Path == "cleanup" {
					return true
				}
			}
			return false
		})
		tree = loadTree(t, st, id)
		byPath := map[string]store.StepNode{}
		for _, n := range tree.Steps {
			byPath[n.Path] = n
		}
		if byPath["s1"].Status != store.StepCanceled {
			t.Fatalf("s1 = %s; want canceled", byPath["s1"].Status)
		}
		if s2 := byPath["s2"]; s2.Status != store.StepCanceled || s2.Error != "job timed out" {
			t.Fatalf("s2 = %s %q; want canceled with reason job timed out", s2.Status, s2.Error)
		}
		// The cleanup step finishes (nobody dispatches it here): the run fails.
		clean := byPath["cleanup"]
		if _, err := st.UpdateStepInstance(ctx, store.StepUpdate{ID: clean.ID, ExpectedStatus: store.StepPending, Status: store.StepSucceeded}, time.Now()); err != nil {
			t.Fatal(err)
		}
		err = st.WithTx(ctx, func(tx store.Tx) error {
			if _, err := tx.TouchRun(ctx, id); err != nil {
				return err
			}
			return a.Enqueue(ctx, tx, id)
		})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "the run to fail", func() bool { return loadTree(t, st, id).Status == store.RunFailed })
	})
}
