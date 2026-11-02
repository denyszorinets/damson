package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// applyCancel runs an advance that cancels the executions at the run's current
// version and returns the run version after it.
func applyCancel(t *testing.T, s store.Store, runID string, at time.Time, ids ...string) error {
	t.Helper()
	ver := runVersion(t, s, runID)
	return withTx(t, s, func(tx store.Tx) error {
		_, err := tx.ApplyAdvance(context.Background(), runID, ver, store.Advance{Cancel: ids}, at)
		return err
	})
}

// requireNoQueuedFlag checks the invariant that a queued row is never flagged.
func requireNoQueuedFlag(t *testing.T, s store.Store, runID string) {
	t.Helper()
	tree, err := s.GetRunTree(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range tree.Steps {
		for _, ex := range st.Executions {
			if ex.State == store.ExecQueued && ex.CancelRequestedAt != nil {
				t.Fatalf("queued execution %s carries cancel_requested_at", ex.ID)
			}
		}
	}
}

func testCancelFlag(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	at := claimNow.Add(time.Minute)

	t.Run("queued is canceled directly", func(t *testing.T) {
		_, tree := mustRun(t, s, "cancel-queued")
		ex := tree.Steps[0].Executions[0]
		if err := applyCancel(t, s, tree.ID, at, ex.ID); err != nil {
			t.Fatal(err)
		}
		got := mustState(t, s, ex.ID, store.ExecCanceled)
		if got.CancelRequestedAt != nil {
			t.Fatalf("canceled queued execution is flagged: %+v", got)
		}
		if st := mustStep(t, s, ex.StepInstanceID); st.Status != store.StepCanceled {
			t.Fatalf("step = %s; want canceled", st.Status)
		}
	})

	t.Run("dispatched is flagged and keeps its state", func(t *testing.T) {
		mustRun(t, s, "cancel-dispatched")
		ex := claim(t, s, "default", 1)[0]
		if err := applyCancel(t, s, ex.RunID, at, ex.ID); err != nil {
			t.Fatal(err)
		}
		got := mustState(t, s, ex.ID, store.ExecDispatched)
		if got.CancelRequestedAt == nil || !got.CancelRequestedAt.Equal(at) || got.AttemptID != ex.AttemptID {
			t.Fatalf("execution = %+v; want flagged at %v under the same attempt", got, at)
		}
		if st := mustStep(t, s, ex.StepInstanceID); st.Status.Terminal() {
			t.Fatalf("step = %s; the owning controller has not cancelled yet", st.Status)
		}

		// A second request keeps the first timestamp.
		if err := applyCancel(t, s, ex.RunID, at.Add(time.Hour), ex.ID); err != nil {
			t.Fatal(err)
		}
		if again := mustState(t, s, ex.ID, store.ExecDispatched); !again.CancelRequestedAt.Equal(at) {
			t.Fatalf("flag moved to %v; want the first request %v", again.CancelRequestedAt, at)
		}

		// Only the owning controller lists it.
		mine, err := s.ListCancelRequested(ctx, "ctrl-1", 10)
		if err != nil || len(mine) != 1 || mine[0].ID != ex.ID {
			t.Fatalf("ListCancelRequested(ctrl-1) = %+v, %v; want the flagged execution", mine, err)
		}
		if other, err := s.ListCancelRequested(ctx, "ctrl-2", 10); err != nil || len(other) != 0 {
			t.Fatalf("ListCancelRequested(ctrl-2) = %+v, %v; want none", other, err)
		}

		// A flagged attempt is not requeued, and the refusal changes nothing.
		if err := s.RequeueExecution(ctx, ex.ID, ex.AttemptID, claimNow); !errors.Is(err, store.ErrStale) {
			t.Fatalf("RequeueExecution of a flagged attempt = %v; want ErrStale", err)
		}
		mustState(t, s, ex.ID, store.ExecDispatched)

		// A wrong attempt cannot cancel it.
		err = withTx(t, s, func(tx store.Tx) error {
			_, err := tx.CancelUndelivered(ctx, ex.ID, "other-attempt", at)
			return err
		})
		if !errors.Is(err, store.ErrStale) {
			t.Fatalf("CancelUndelivered with a wrong attempt = %v; want ErrStale", err)
		}

		before := runVersion(t, s, ex.RunID)
		err = withTx(t, s, func(tx store.Tx) error {
			got, err := tx.CancelUndelivered(ctx, ex.ID, ex.AttemptID, at)
			if err == nil && got.State != store.ExecCanceled {
				t.Errorf("returned state = %s; want canceled", got.State)
			}
			return err
		})
		if err != nil {
			t.Fatalf("CancelUndelivered: %v", err)
		}
		mustState(t, s, ex.ID, store.ExecCanceled)
		if st := mustStep(t, s, ex.StepInstanceID); st.Status != store.StepCanceled {
			t.Fatalf("step = %s; want canceled", st.Status)
		}
		if after := runVersion(t, s, ex.RunID); after != before+1 {
			t.Fatalf("run version = %d; want %d (the run must wake)", after, before+1)
		}
		if left, _ := s.ListCancelRequested(ctx, "ctrl-1", 10); len(left) != 0 {
			t.Fatalf("a canceled execution is still listed: %+v", left)
		}
		requireNoQueuedFlag(t, s, ex.RunID)
	})

	t.Run("running is flagged and ends only by report", func(t *testing.T) {
		mustRun(t, s, "cancel-running")
		ex := claim(t, s, "default", 1)[0]
		if err := s.MarkExecutionRunning(ctx, ex.ID, ex.AttemptID, claimNow); err != nil {
			t.Fatal(err)
		}
		if err := applyCancel(t, s, ex.RunID, at, ex.ID); err != nil {
			t.Fatal(err)
		}
		if got := mustState(t, s, ex.ID, store.ExecRunning); got.CancelRequestedAt == nil {
			t.Fatal("running execution not flagged")
		}
		err := withTx(t, s, func(tx store.Tx) error {
			_, err := tx.CancelUndelivered(ctx, ex.ID, ex.AttemptID, at)
			return err
		})
		if !errors.Is(err, store.ErrStale) {
			t.Fatalf("CancelUndelivered of a running attempt = %v; want ErrStale", err)
		}
		// The plugin finished anyway: the report is applied as reported.
		err = withTx(t, s, func(tx store.Tx) error {
			_, err := tx.CompleteExecution(ctx, store.Completion{ExecutionID: ex.ID, AttemptID: ex.AttemptID, State: store.ExecSucceeded}, at)
			return err
		})
		if err != nil {
			t.Fatalf("CompleteExecution of a flagged attempt: %v", err)
		}
		mustState(t, s, ex.ID, store.ExecSucceeded)
		if st := mustStep(t, s, ex.StepInstanceID); st.Status != store.StepSucceeded {
			t.Fatalf("step = %s; want succeeded", st.Status)
		}
	})

	t.Run("not flagged and terminal are left alone", func(t *testing.T) {
		mustRun(t, s, "cancel-plain")
		ex := claim(t, s, "default", 1)[0]
		err := withTx(t, s, func(tx store.Tx) error {
			_, err := tx.CancelUndelivered(ctx, ex.ID, ex.AttemptID, at)
			return err
		})
		if !errors.Is(err, store.ErrStale) {
			t.Fatalf("CancelUndelivered of an unflagged attempt = %v; want ErrStale", err)
		}
		mustState(t, s, ex.ID, store.ExecDispatched)

		err = withTx(t, s, func(tx store.Tx) error {
			_, err := tx.CompleteExecution(ctx, store.Completion{ExecutionID: ex.ID, AttemptID: ex.AttemptID, State: store.ExecFailed, Error: "x"}, at)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := applyCancel(t, s, ex.RunID, at, ex.ID); err != nil {
			t.Fatal(err)
		}
		if got := mustState(t, s, ex.ID, store.ExecFailed); got.CancelRequestedAt != nil {
			t.Fatalf("a finished execution was flagged: %+v", got)
		}
	})

	t.Run("an execution of another run is not found", func(t *testing.T) {
		_, a := mustRun(t, s, "cancel-run-a")
		_, b := mustRun(t, s, "cancel-run-b")
		err := applyCancel(t, s, a.ID, at, b.Steps[0].Executions[0].ID)
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("cancel of another run's execution = %v; want ErrNotFound", err)
		}
	})
}
