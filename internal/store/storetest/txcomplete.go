package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func mustStep(t *testing.T, s store.Store, id string) store.StepInstance {
	t.Helper()
	st, err := s.GetStepInstance(context.Background(), id)
	if err != nil {
		t.Fatalf("GetStepInstance: %v", err)
	}
	return st
}

func runVersion(t *testing.T, s store.Store, id string) int64 {
	t.Helper()
	r, err := s.GetRun(context.Background(), id)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	return r.Version
}

func testTxCompleteExecution(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	ex := claimedOne(t, s)
	if err := s.MarkExecutionRunning(ctx, ex.ID, ex.AttemptID, claimNow); err != nil {
		t.Fatal(err)
	}
	before := runVersion(t, s, ex.RunID)

	// A stale attempt changes nothing: no execution, step or version change.
	err := withTx(t, s, func(tx store.Tx) error {
		_, err := tx.CompleteExecution(ctx, store.Completion{ExecutionID: ex.ID, AttemptID: "old", State: store.ExecSucceeded}, claimNow)
		return err
	})
	if !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale CompleteExecution = %v; want ErrStale", err)
	}
	if got := mustStep(t, s, ex.StepInstanceID); got.Status.Terminal() {
		t.Fatalf("step = %s after stale completion", got.Status)
	}

	err = withTx(t, s, func(tx store.Tx) error {
		_, err := tx.CompleteExecution(ctx, store.Completion{
			ExecutionID: ex.ID, AttemptID: ex.AttemptID, State: store.ExecSucceeded, Outputs: json.RawMessage(`{"rc":0}`),
		}, claimNow)
		return err
	})
	if err != nil {
		t.Fatalf("CompleteExecution: %v", err)
	}
	mustState(t, s, ex.ID, store.ExecSucceeded)
	st := mustStep(t, s, ex.StepInstanceID)
	if st.Status != store.StepSucceeded || string(st.Outputs) != `{"rc":0}` {
		t.Fatalf("step = %s %s; want succeeded with outputs", st.Status, st.Outputs)
	}
	if after := runVersion(t, s, ex.RunID); after != before+1 {
		t.Fatalf("run version = %d; want %d", after, before+1)
	}

	// A duplicate is stale and leaves the step alone.
	err = withTx(t, s, func(tx store.Tx) error {
		_, err := tx.CompleteExecution(ctx, store.Completion{ExecutionID: ex.ID, AttemptID: ex.AttemptID, State: store.ExecFailed}, claimNow)
		return err
	})
	if !errors.Is(err, store.ErrStale) {
		t.Fatalf("duplicate CompleteExecution = %v; want ErrStale", err)
	}
	if got := mustStep(t, s, ex.StepInstanceID); got.Status != store.StepSucceeded {
		t.Fatalf("step = %s after duplicate", got.Status)
	}
}

func testTxExpireLeases(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	ex := claimedOne(t, s)
	before := runVersion(t, s, ex.RunID)

	var lost []store.Execution
	if err := withTx(t, s, func(tx store.Tx) (err error) {
		lost, err = tx.ExpireLeases(ctx, claimNow.Add(leaseTTL), 10)
		return err
	}); err != nil || len(lost) != 0 {
		t.Fatalf("ExpireLeases at lease end = %v, %v; want none", lost, err)
	}

	late := claimNow.Add(leaseTTL + time.Second)
	if err := withTx(t, s, func(tx store.Tx) (err error) {
		lost, err = tx.ExpireLeases(ctx, late, 10)
		return err
	}); err != nil || len(lost) != 1 || lost[0].ID != ex.ID {
		t.Fatalf("ExpireLeases = %v, %v; want the claimed execution", lost, err)
	}
	mustState(t, s, ex.ID, store.ExecLost)
	if st := mustStep(t, s, ex.StepInstanceID); st.Status != store.StepFailed || st.Error == "" {
		t.Fatalf("step = %s %q; want failed with a reason", st.Status, st.Error)
	}
	if after := runVersion(t, s, ex.RunID); after != before+1 {
		t.Fatalf("run version = %d; want %d", after, before+1)
	}
}
