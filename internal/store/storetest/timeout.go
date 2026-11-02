package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func timeoutStep(path string, timeout time.Duration) store.NewStepInstance {
	return store.NewStepInstance{Path: path, Plugin: "shell", Input: json.RawMessage(`{"cmd":"true"}`),
		Execution: &store.NewExecution{Pool: "default", ScheduledAt: t0, Timeout: timeout}}
}

func testExecutionTimeout(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)

	// The timeout is stored in whole milliseconds, rounded up, and survives
	// the claim.
	_, tree := mustRun(t, s, "timeouts", timeoutStep("a", 1500*time.Millisecond), timeoutStep("b", 500*time.Microsecond), timeoutStep("c", 0))
	want := map[string]time.Duration{"a": 1500 * time.Millisecond, "b": time.Millisecond, "c": 0}
	for _, n := range tree.Steps {
		if got := n.Executions[0].Timeout; got != want[n.Path] {
			t.Fatalf("step %s execution timeout = %v; want %v", n.Path, got, want[n.Path])
		}
	}
	claimed := claim(t, s, "default", 3)
	if len(claimed) != 3 {
		t.Fatalf("claimed %d; want 3", len(claimed))
	}
	for _, ex := range claimed {
		stored, err := s.GetExecution(ctx, ex.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Timeout != ex.Timeout {
			t.Fatalf("claimed timeout %v != stored %v", ex.Timeout, stored.Timeout)
		}
	}
	byStep := map[string]store.Execution{}
	for _, ex := range claimed {
		byStep[ex.StepInstanceID] = ex
	}
	for _, n := range tree.Steps {
		if got := byStep[n.ID].Timeout; got != want[n.Path] {
			t.Fatalf("claimed step %s timeout = %v; want %v", n.Path, got, want[n.Path])
		}
	}
}

func testStepErrorKinds(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	at := claimNow.Add(time.Minute)

	complete := func(name string, state store.ExecutionState, errText string) store.StepInstance {
		t.Helper()
		mustRun(t, s, name)
		ex := claim(t, s, "default", 1)[0]
		err := withTx(t, s, func(tx store.Tx) error {
			_, err := tx.CompleteExecution(ctx, store.Completion{ExecutionID: ex.ID, AttemptID: ex.AttemptID, State: state, Error: errText}, at)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return mustStep(t, s, ex.StepInstanceID)
	}

	tests := []struct {
		name     string
		state    store.ExecutionState
		errText  string
		wantKind string
		wantMsg  string
	}{
		{"timed out", store.ExecTimedOut, store.StepErrorText("timeout", "step timed out"), "timeout", "step timed out"},
		{"failed keeps the plugin's code", store.ExecFailed, store.StepErrorText("exit_code", "exit 2"), "exit_code", "exit 2"},
		{"legacy plain failed", store.ExecFailed, "boom", "error", "boom"},
		{"timed out without text", store.ExecTimedOut, "", "timeout", "timed_out"},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := complete("kinds-"+string(rune('a'+i)), tc.state, tc.errText)
			if st.Status != store.StepFailed {
				t.Fatalf("step = %s; want failed", st.Status)
			}
			kind, msg := store.SplitStepError(st.Error)
			if kind != tc.wantKind || msg != tc.wantMsg {
				t.Fatalf("step error = kind %q message %q (raw %q); want %q %q", kind, msg, st.Error, tc.wantKind, tc.wantMsg)
			}
		})
	}

	t.Run("lost by the reaper", func(t *testing.T) {
		mustRun(t, s, "kinds-lost")
		ex := claim(t, s, "default", 1)[0]
		var lost []store.Execution
		err := withTx(t, s, func(tx store.Tx) error {
			var err error
			lost, err = tx.ExpireLeases(ctx, claimNow.Add(2*leaseTTL), 10)
			return err
		})
		if err != nil || len(lost) == 0 {
			t.Fatalf("ExpireLeases = %v, %v; want the attempt lost", lost, err)
		}
		st := mustStep(t, s, ex.StepInstanceID)
		if kind, _ := store.SplitStepError(st.Error); kind != store.ErrKindLost {
			t.Fatalf("step error = %q; want kind lost", st.Error)
		}
	})
}

func testRunCancelInAdvance(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	at := claimNow.Add(time.Minute)

	apply := func(runID string, version int64, a store.Advance) (store.Run, error) {
		var run store.Run
		err := withTx(t, s, func(tx store.Tx) error {
			var err error
			run, err = tx.ApplyAdvance(ctx, runID, version, a, at)
			return err
		})
		return run, err
	}

	t.Run("the timeout is a sticky cancel with a reason", func(t *testing.T) {
		_, tree := mustRun(t, s, "timeout-run")
		ver := runVersion(t, s, tree.ID)
		run, err := apply(tree.ID, ver, store.Advance{CancelRun: store.CancelReasonTimeout})
		if err != nil {
			t.Fatal(err)
		}
		if run.CancelRequestedAt == nil || !run.CancelRequestedAt.Equal(at) || run.CancelReason != store.CancelReasonTimeout || run.Version != ver+1 {
			t.Fatalf("run = %+v; want cancelled at %v with reason timeout, version %d", run, at, ver+1)
		}
		// A user cancel after the timeout is refused as already requested.
		err = withTx(t, s, func(tx store.Tx) error { _, err := tx.RequestCancel(ctx, tree.ID, at); return err })
		if !errors.Is(err, store.ErrStale) {
			t.Fatalf("RequestCancel after a timeout = %v; want ErrStale", err)
		}
		if got, _ := s.GetRun(ctx, tree.ID); got.CancelReason != store.CancelReasonTimeout {
			t.Fatalf("reason after a user cancel = %q; want it kept", got.CancelReason)
		}
	})

	t.Run("a user cancel that commits first wins", func(t *testing.T) {
		_, tree := mustRun(t, s, "timeout-vs-user")
		ver := runVersion(t, s, tree.ID)
		err := withTx(t, s, func(tx store.Tx) error { _, err := tx.RequestCancel(ctx, tree.ID, at); return err })
		if err != nil {
			t.Fatal(err)
		}
		// The advance read the run at ver, before the user cancel: it loses the
		// version CAS, so the timeout is not recorded over the user's cancel.
		if _, err := apply(tree.ID, ver, store.Advance{CancelRun: store.CancelReasonTimeout}); !errors.Is(err, store.ErrStale) {
			t.Fatalf("timeout advance after a user cancel = %v; want ErrStale", err)
		}
		if got, _ := s.GetRun(ctx, tree.ID); got.CancelReason != "" {
			t.Fatalf("reason = %q; want the user cancel's empty reason", got.CancelReason)
		}
	})

	t.Run("timeout and finish in one advance", func(t *testing.T) {
		// The deadline passed with nothing left to cancel: the same advance
		// records the timeout and finishes the run. The finish moves the run
		// to a terminal status first; the reason must still be recorded.
		_, tree := mustRun(t, s, "timeout-and-finish")
		ver := runVersion(t, s, tree.ID)
		run, err := apply(tree.ID, ver, store.Advance{CancelRun: store.CancelReasonTimeout, Finish: store.RunFailed})
		if err != nil {
			t.Fatalf("timeout and finish in one advance: %v", err)
		}
		if run.Status != store.RunFailed || run.CancelReason != store.CancelReasonTimeout || run.CancelRequestedAt == nil || run.Version != ver+1 {
			t.Fatalf("run = %+v; want failed, cancelled by timeout, version %d", run, ver+1)
		}
	})

	t.Run("an unknown reason is invalid", func(t *testing.T) {
		_, tree := mustRun(t, s, "timeout-bad")
		if _, err := apply(tree.ID, runVersion(t, s, tree.ID), store.Advance{CancelRun: "nope"}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("unknown reason = %v; want ErrInvalid", err)
		}
	})
}
