package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// applyAdvance runs one advance at the run's current version.
func applyAdvance(t *testing.T, s store.Store, runID string, a store.Advance, at time.Time) error {
	t.Helper()
	ver := runVersion(t, s, runID)
	return withTx(t, s, func(tx store.Tx) error {
		_, err := tx.ApplyAdvance(context.Background(), runID, ver, a, at)
		return err
	})
}

func completeAttempt(t *testing.T, s store.Store, ex store.Execution, state store.ExecutionState, errText string, at time.Time) {
	t.Helper()
	err := withTx(t, s, func(tx store.Tx) error {
		_, err := tx.CompleteExecution(context.Background(), store.Completion{ExecutionID: ex.ID, AttemptID: ex.AttemptID, State: state, Error: errText}, at)
		return err
	})
	if err != nil {
		t.Fatalf("complete attempt: %v", err)
	}
}

func testRetryAttempts(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	at := claimNow.Add(time.Minute)

	t.Run("a failed attempt leaves the step open and wakes the run", func(t *testing.T) {
		mustRun(t, s, "retry-open")
		ex := claim(t, s, "default", 1)[0]
		before := runVersion(t, s, ex.RunID)
		completeAttempt(t, s, ex, store.ExecFailed, store.StepErrorText("connection", "reset"), at)
		if st := mustStep(t, s, ex.StepInstanceID); st.Status.Terminal() {
			t.Fatalf("step = %s after a failed attempt; want it left open", st.Status)
		}
		if after := runVersion(t, s, ex.RunID); after != before+1 {
			t.Fatalf("run version = %d; want %d so the advance runs", after, before+1)
		}
	})

	t.Run("succeeded and canceled attempts still finish the step", func(t *testing.T) {
		mustRun(t, s, "retry-ok")
		ex := claim(t, s, "default", 1)[0]
		completeAttempt(t, s, ex, store.ExecSucceeded, "", at)
		if st := mustStep(t, s, ex.StepInstanceID); st.Status != store.StepSucceeded {
			t.Fatalf("step = %s; want succeeded", st.Status)
		}
		mustRun(t, s, "retry-canceled")
		ex = claim(t, s, "default", 1)[0]
		completeAttempt(t, s, ex, store.ExecCanceled, "", at)
		if st := mustStep(t, s, ex.StepInstanceID); st.Status != store.StepCanceled {
			t.Fatalf("step = %s; want canceled", st.Status)
		}
	})

	t.Run("settle finishes the step from the attempt's outcome", func(t *testing.T) {
		mustRun(t, s, "retry-settle")
		ex := claim(t, s, "default", 1)[0]
		completeAttempt(t, s, ex, store.ExecFailed, store.StepErrorText("exit_code", "exit 2"), at)
		step := mustStep(t, s, ex.StepInstanceID)

		// Not the latest attempt, or an unknown path: refused.
		for _, bad := range []store.AdvanceSettle{{Path: step.Path, Attempt: 2}, {Path: step.Path, Attempt: 7}} {
			if err := applyAdvance(t, s, ex.RunID, store.Advance{Settle: []store.AdvanceSettle{bad}}, at); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("Settle %+v = %v; want ErrConflict", bad, err)
			}
		}
		if err := applyAdvance(t, s, ex.RunID, store.Advance{Settle: []store.AdvanceSettle{{Path: "nope", Attempt: 1}}}, at); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("Settle of an unknown step = %v; want ErrNotFound", err)
		}
		if mustStep(t, s, ex.StepInstanceID).Status.Terminal() {
			t.Fatal("a refused settle changed the step")
		}

		if err := applyAdvance(t, s, ex.RunID, store.Advance{Settle: []store.AdvanceSettle{{Path: step.Path, Attempt: 1}}}, at); err != nil {
			t.Fatalf("Settle: %v", err)
		}
		got := mustStep(t, s, ex.StepInstanceID)
		if kind, msg := store.SplitStepError(got.Error); got.Status != store.StepFailed || kind != "exit_code" || msg != "exit 2" {
			t.Fatalf("step = %s %q; want failed with the attempt's error", got.Status, got.Error)
		}
		// The step is finished: a second settle, or an attempt, conflicts.
		if err := applyAdvance(t, s, ex.RunID, store.Advance{Settle: []store.AdvanceSettle{{Path: step.Path, Attempt: 1}}}, at); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("second Settle = %v; want ErrConflict", err)
		}
	})

	t.Run("settle needs a finished attempt", func(t *testing.T) {
		mustRun(t, s, "retry-settle-live")
		ex := claim(t, s, "default", 1)[0]
		step := mustStep(t, s, ex.StepInstanceID)
		if err := applyAdvance(t, s, ex.RunID, store.Advance{Settle: []store.AdvanceSettle{{Path: step.Path, Attempt: 1}}}, at); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("Settle of a dispatched attempt = %v; want ErrConflict", err)
		}
	})

	t.Run("a retry is attempt n+1, due later, and refused when it does not follow", func(t *testing.T) {
		mustRun(t, s, "retry-next")
		first := claim(t, s, "default", 1)[0]
		step := mustStep(t, s, first.StepInstanceID)
		next := func(attempt int, due time.Time) store.Advance {
			return store.Advance{NewAttempts: []store.AdvanceAttempt{{Path: step.Path, Attempt: attempt, NewExecution: store.NewExecution{Pool: "default", ScheduledAt: due, Timeout: 2 * time.Second}}}}
		}
		// The first attempt is still running: no second one.
		if err := applyAdvance(t, s, first.RunID, next(2, at), at); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("attempt 2 after a live attempt 1 = %v; want ErrConflict", err)
		}
		completeAttempt(t, s, first, store.ExecFailed, store.StepErrorText("connection", "reset"), at)
		// A number that does not follow: refused.
		for _, n := range []int{1, 3} {
			if err := applyAdvance(t, s, first.RunID, next(n, at), at); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("attempt %d after a failed attempt 1 = %v; want ErrConflict", n, err)
			}
		}
		due := claimNow.Add(time.Hour)
		if err := applyAdvance(t, s, first.RunID, next(2, due), at); err != nil {
			t.Fatalf("attempt 2: %v", err)
		}
		// The same decision twice cannot make attempt 2 twice.
		if err := applyAdvance(t, s, first.RunID, next(2, due), at); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate attempt 2 = %v; want ErrConflict", err)
		}
		tree, err := s.GetRunTree(ctx, first.RunID)
		if err != nil {
			t.Fatal(err)
		}
		var second store.Execution
		for _, n := range tree.Steps {
			if n.ID == first.StepInstanceID {
				if len(n.Executions) != 2 {
					t.Fatalf("step has %d attempts; want 2", len(n.Executions))
				}
				second = n.Executions[1]
			}
		}
		if second.AttemptNo != 2 || second.AttemptID == first.AttemptID || second.State != store.ExecQueued ||
			!second.ScheduledAt.Equal(due) || second.Timeout != 2*time.Second {
			t.Fatalf("attempt 2 = %+v; want queued, due %v, a new attempt id, the timeout", second, due)
		}
		// Not claimed before it is due, claimed once it is.
		if got, err := s.ClaimExecutions(ctx, store.ClaimParams{AgentID: "agent-1", ControllerID: "ctrl-1", Pools: []string{"default"}, Limit: 5, LeaseTTL: leaseTTL}, claimNow); err != nil || len(got) != 0 {
			t.Fatalf("claim before the retry is due = %v, %v; want none", got, err)
		}
		got, err := s.ClaimExecutions(ctx, store.ClaimParams{AgentID: "agent-1", ControllerID: "ctrl-1", Pools: []string{"default"}, Limit: 5, LeaseTTL: leaseTTL}, due)
		if err != nil || len(got) != 1 || got[0].ID != second.ID {
			t.Fatalf("claim once due = %v, %v; want attempt 2", got, err)
		}
		// A late report of attempt 1 is stale: it changes nothing.
		err = withTx(t, s, func(tx store.Tx) error {
			_, err := tx.CompleteExecution(ctx, store.Completion{ExecutionID: first.ID, AttemptID: first.AttemptID, State: store.ExecSucceeded}, due)
			return err
		})
		if !errors.Is(err, store.ErrStale) {
			t.Fatalf("late completion of attempt 1 = %v; want ErrStale", err)
		}
	})

	t.Run("a finished step takes no attempt", func(t *testing.T) {
		mustRun(t, s, "retry-finished")
		ex := claim(t, s, "default", 1)[0]
		completeAttempt(t, s, ex, store.ExecSucceeded, "", at)
		step := mustStep(t, s, ex.StepInstanceID)
		a := store.Advance{NewAttempts: []store.AdvanceAttempt{{Path: step.Path, Attempt: 2, NewExecution: *execSpec("default")}}}
		if err := applyAdvance(t, s, ex.RunID, a, at); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("attempt on a succeeded step = %v; want ErrConflict", err)
		}
	})

	t.Run("invalid change sets are rejected before any write", func(t *testing.T) {
		_, tree := mustRun(t, s, "retry-invalid")
		path := tree.Steps[0].Path
		bad := []store.Advance{
			{Settle: []store.AdvanceSettle{{Path: path, Attempt: 0}}},
			{NewAttempts: []store.AdvanceAttempt{{Path: path, Attempt: 0, NewExecution: *execSpec("default")}}},
			{Settle: []store.AdvanceSettle{{Path: path, Attempt: 1}}, NewAttempts: []store.AdvanceAttempt{{Path: path, Attempt: 2, NewExecution: *execSpec("default")}}},
		}
		for i, a := range bad {
			if err := store.ValidateAdvance(a); err == nil {
				t.Errorf("case %d: ValidateAdvance accepted %+v", i, a)
			}
		}
	})
}
