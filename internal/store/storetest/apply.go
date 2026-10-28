package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

var errRollback = errors.New("rollback on purpose")

func advanceStep(path string, status store.StepStatus) store.AdvanceStep {
	st := store.AdvanceStep{Path: path, Plugin: "shell", Input: json.RawMessage(`{"cmd":"true"}`), Status: status}
	if status == "" || status == store.StepPending {
		st.Execution = execSpec("default")
	} else {
		st.Reason = "because"
	}
	return st
}

func withTx(t *testing.T, s store.Store, fn func(tx store.Tx) error) error {
	t.Helper()
	return s.WithTx(context.Background(), fn)
}

func testWithTxRollback(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	job, ver := mustJob(t, s, "tx")
	var id string
	err := withTx(t, s, func(tx store.Tx) error {
		tree, err := tx.CreateRun(ctx, store.NewRun{JobID: job.ID, JobVersionID: ver.ID}, t0)
		id = tree.ID
		if err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("WithTx = %v, want errRollback", err)
	}
	if _, err := s.GetRun(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after rollback = %v, want ErrNotFound", err)
	}
	err = withTx(t, s, func(tx store.Tx) error {
		tree, err := tx.CreateRun(ctx, store.NewRun{JobID: job.ID, JobVersionID: ver.ID}, t0)
		id = tree.ID
		return err
	})
	if err != nil {
		t.Fatalf("WithTx commit: %v", err)
	}
	if _, err := s.GetRun(ctx, id); err != nil {
		t.Fatalf("GetRun after commit: %v", err)
	}
}

func testApplyAdvance(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	job, ver := mustJob(t, s, "apply")
	tree, err := s.CreateRun(ctx, store.NewRun{JobID: job.ID, JobVersionID: ver.ID}, t0)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(version int64, a store.Advance) (store.Run, error) {
		var run store.Run
		err := withTx(t, s, func(tx store.Tx) error {
			var err error
			run, err = tx.ApplyAdvance(ctx, tree.ID, version, a, t0.Add(time.Minute))
			return err
		})
		return run, err
	}

	// An empty advance changes nothing and keeps the version.
	if run, err := apply(1, store.Advance{}); err != nil || run.Version != 1 {
		t.Fatalf("empty advance = v%d, %v; want v1, nil", run.Version, err)
	}
	// Start + a skipped step + a pending step: one version bump.
	run, err := apply(1, store.Advance{
		Start:    true,
		NewSteps: []store.AdvanceStep{advanceStep("a", store.StepSkipped), advanceStep("b", "")},
	})
	if err != nil || run.Version != 2 || run.Status != store.RunRunning || run.StartedAt == nil {
		t.Fatalf("start advance = %+v, %v", run, err)
	}
	got, err := s.GetRunTree(ctx, tree.ID)
	if err != nil || len(got.Steps) != 2 {
		t.Fatalf("tree = %+v, %v", got, err)
	}
	a, b := got.Steps[0], got.Steps[1]
	if a.Path != "a" || a.Status != store.StepSkipped || a.Error != "because" || a.FinishedAt == nil || len(a.Executions) != 0 {
		t.Errorf("skipped step = %+v", a)
	}
	if b.Path != "b" || b.Status != store.StepPending || len(b.Executions) != 1 || b.Executions[0].State != store.ExecQueued {
		t.Errorf("pending step = %+v", b)
	}
	// A stale version loses and changes nothing.
	if _, err := apply(1, store.Advance{NewSteps: []store.AdvanceStep{advanceStep("c", "")}}); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale advance = %v, want ErrStale", err)
	}
	// Canceling the queued execution cancels its step; finishing ends the run.
	run, err = apply(2, store.Advance{CancelQueued: []string{b.Executions[0].ID}, Finish: store.RunCanceled})
	if err != nil || run.Version != 3 || run.Status != store.RunCanceled || run.FinishedAt == nil {
		t.Fatalf("cancel advance = %+v, %v", run, err)
	}
	got, _ = s.GetRunTree(ctx, tree.ID)
	if st := got.Steps[1]; st.Status != store.StepCanceled || st.Executions[0].State != store.ExecCanceled {
		t.Errorf("cancelled step = %+v", st)
	}
	// A terminal run rejects further advances.
	if _, err := apply(3, store.Advance{Start: true}); !errors.Is(err, store.ErrStale) {
		t.Fatalf("advance of terminal run = %v, want ErrStale", err)
	}
	if _, err := apply(1, store.Advance{Start: true}); err == nil {
		t.Fatal("advance of terminal run succeeded")
	}
}

func testApplyAdvanceAtomic(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	job, ver := mustJob(t, s, "atomic")
	tree, err := s.CreateRun(ctx, store.NewRun{JobID: job.ID, JobVersionID: ver.ID}, t0)
	if err != nil {
		t.Fatal(err)
	}
	// The second step collides with the first inside the same advance.
	err = withTx(t, s, func(tx store.Tx) error {
		_, err := tx.ApplyAdvance(ctx, tree.ID, 1, store.Advance{
			Start:    true,
			NewSteps: []store.AdvanceStep{advanceStep("a", ""), advanceStep("b", "")},
			NewAttempts: []store.AdvanceAttempt{
				{Path: "missing", NewExecution: *execSpec("default")},
			},
		}, t0)
		return err
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("advance with a missing attempt step = %v, want ErrNotFound", err)
	}
	got, err := s.GetRunTree(ctx, tree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.Status != store.RunPending || len(got.Steps) != 0 {
		t.Fatalf("partial apply leaked: %+v", got)
	}
	// Invalid change sets are rejected before any write.
	for _, a := range []store.Advance{
		{Finish: store.RunRunning},
		{NewSteps: []store.AdvanceStep{{Path: "x"}}},
		{NewSteps: []store.AdvanceStep{advanceStep("x", ""), advanceStep("x", "")}},
	} {
		err := withTx(t, s, func(tx store.Tx) error {
			_, err := tx.ApplyAdvance(ctx, tree.ID, 1, a, t0)
			return err
		})
		if err == nil {
			t.Errorf("advance %+v succeeded", a)
		}
	}
}

func testApplyAdvanceRace(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	job, ver := mustJob(t, s, "race")
	tree, err := s.CreateRun(ctx, store.NewRun{JobID: job.ID, JobVersionID: ver.ID}, t0)
	if err != nil {
		t.Fatal(err)
	}
	res := raceN(8, func(int) error {
		return s.WithTx(ctx, func(tx store.Tx) error {
			_, err := tx.ApplyAdvance(ctx, tree.ID, 1, store.Advance{Start: true, NewSteps: []store.AdvanceStep{advanceStep("a", "")}}, t0)
			return err
		})
	})
	if res.won != 1 || res.won+res.stale != 8 || len(res.other) != 0 {
		t.Fatalf("race = %+v, want exactly one winner", res)
	}
	got, _ := s.GetRunTree(ctx, tree.ID)
	if got.Version != 2 || len(got.Steps) != 1 || len(got.Steps[0].Executions) != 1 {
		t.Fatalf("after race: %+v", got)
	}
}

func testActiveRunsAndTouch(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	job, ver := mustJob(t, s, "active")
	var ids []string
	for range 3 {
		tree, err := s.CreateRun(ctx, store.NewRun{JobID: job.ID, JobVersionID: ver.ID}, t0)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tree.ID)
	}
	if _, err := s.UpdateRunStatus(ctx, ids[1], 1, store.RunSucceeded, t0); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListActiveRunIDs(ctx, "", 10)
	if err != nil || len(got) != 2 || got[0] != ids[0] || got[1] != ids[2] {
		t.Fatalf("active = %v, %v; want %v", got, err, []string{ids[0], ids[2]})
	}
	if got, _ := s.ListActiveRunIDs(ctx, ids[0], 1); len(got) != 1 || got[0] != ids[2] {
		t.Fatalf("page after first = %v", got)
	}
	var v int64
	err = s.WithTx(ctx, func(tx store.Tx) error {
		var err error
		v, err = tx.TouchRun(ctx, ids[0])
		return err
	})
	if err != nil || v != 2 {
		t.Fatalf("TouchRun = %d, %v; want 2", v, err)
	}
	err = s.WithTx(ctx, func(tx store.Tx) error { _, err := tx.TouchRun(ctx, ids[1]); return err })
	if !errors.Is(err, store.ErrStale) {
		t.Fatalf("TouchRun of finished run = %v, want ErrStale", err)
	}
	err = s.WithTx(ctx, func(tx store.Tx) error { _, err := tx.TouchRun(ctx, "nope"); return err })
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("TouchRun of missing run = %v, want ErrNotFound", err)
	}
}
