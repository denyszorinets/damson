package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func groupStep(path string) store.AdvanceStep {
	return store.AdvanceStep{Path: path, Kind: store.KindGroup}
}

func leafStep(path string) store.AdvanceStep {
	return store.AdvanceStep{Path: path, Plugin: "shell", Input: json.RawMessage(`{"cmd":"true"}`), Execution: execSpec("default")}
}

func stepAt(t *testing.T, s store.Store, runID, path string) store.StepNode {
	t.Helper()
	tree, err := s.GetRunTree(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range tree.Steps {
		if n.Path == path {
			return n
		}
	}
	t.Fatalf("run %s has no step %q", runID, path)
	return store.StepNode{}
}

func testGroups(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	at := claimNow.Add(time.Minute)
	newRunID := func(name string) string {
		_, tree := mustRun(t, s, name)
		return tree.ID
	}

	t.Run("a group starts running and its steps follow", func(t *testing.T) {
		id := newRunID("g-start")
		err := applyAdvance(t, s, id, store.Advance{NewSteps: []store.AdvanceStep{groupStep("deploy"), leafStep("deploy/unpack")}}, at)
		if err != nil {
			t.Fatal(err)
		}
		g := stepAt(t, s, id, "deploy")
		if g.Kind != store.KindGroup || g.Status != store.StepRunning || g.StartedAt == nil || !g.StartedAt.Equal(at) || len(g.Executions) != 0 {
			t.Fatalf("group = %+v; want a running group started at %v with no execution", g.StepInstance, at)
		}
		if leaf := stepAt(t, s, id, "deploy/unpack"); leaf.Kind != store.KindStep || len(leaf.Executions) != 1 {
			t.Fatalf("child = %+v", leaf)
		}
	})

	t.Run("a group is finished with its error and keeps started_at", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			status store.StepStatus
			errTxt string
		}{
			{"succeeded", store.StepSucceeded, ""},
			{"failed", store.StepFailed, `{"kind":"exit_code","message":"boom","path":"g/x"}`},
			{"canceled", store.StepCanceled, ""},
		} {
			id := newRunID("g-finish-" + tc.name)
			if err := applyAdvance(t, s, id, store.Advance{NewSteps: []store.AdvanceStep{groupStep("g")}}, at); err != nil {
				t.Fatal(err)
			}
			done := at.Add(time.Minute)
			if err := applyAdvance(t, s, id, store.Advance{FinishGroups: []store.AdvanceFinishGroup{{Path: "g", Status: tc.status, Error: tc.errTxt}}}, done); err != nil {
				t.Fatalf("%s: FinishGroups: %v", tc.name, err)
			}
			g := stepAt(t, s, id, "g")
			if g.Status != tc.status || g.Error != tc.errTxt || g.StartedAt == nil || !g.StartedAt.Equal(at) || g.FinishedAt == nil || !g.FinishedAt.Equal(done) {
				t.Fatalf("%s: group = %+v; want %s, error %q, started %v, finished %v", tc.name, g.StepInstance, tc.status, tc.errTxt, at, done)
			}
		}
	})

	t.Run("a group can be created and finished in one advance", func(t *testing.T) {
		id := newRunID("g-one-shot")
		skipped := store.AdvanceStep{Path: "g/a", Status: store.StepSkipped, Reason: "when condition is false"}
		err := applyAdvance(t, s, id, store.Advance{
			NewSteps:     []store.AdvanceStep{groupStep("g"), skipped},
			FinishGroups: []store.AdvanceFinishGroup{{Path: "g", Status: store.StepSucceeded}},
		}, at)
		if err != nil {
			t.Fatal(err)
		}
		if g := stepAt(t, s, id, "g"); g.Status != store.StepSucceeded || g.FinishedAt == nil {
			t.Fatalf("group = %+v", g.StepInstance)
		}
	})

	t.Run("a group may be created already skipped", func(t *testing.T) {
		id := newRunID("g-skipped")
		skip := store.AdvanceStep{Path: "g", Kind: store.KindGroup, Status: store.StepSkipped, Reason: "when condition is false"}
		if err := applyAdvance(t, s, id, store.Advance{NewSteps: []store.AdvanceStep{skip}}, at); err != nil {
			t.Fatal(err)
		}
		if g := stepAt(t, s, id, "g"); g.Status != store.StepSkipped || g.Kind != store.KindGroup || g.StartedAt != nil {
			t.Fatalf("group = %+v; want skipped and never started", g.StepInstance)
		}
	})

	t.Run("finishing refuses what is not an open group", func(t *testing.T) {
		id := newRunID("g-conflicts")
		if err := applyAdvance(t, s, id, store.Advance{NewSteps: []store.AdvanceStep{groupStep("g"), leafStep("g/x")}}, at); err != nil {
			t.Fatal(err)
		}
		finish := func(path string) error {
			return applyAdvance(t, s, id, store.Advance{FinishGroups: []store.AdvanceFinishGroup{{Path: path, Status: store.StepSucceeded}}}, at)
		}
		for _, path := range []string{"g/x", "nope", "main/a"} { // a plugin step, an unknown path
			if err := finish(path); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("FinishGroups(%q) = %v; want ErrConflict", path, err)
			}
		}
		if err := finish("g"); err != nil {
			t.Fatal(err)
		}
		if err := finish("g"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("finishing a finished group = %v; want ErrConflict", err)
		}
	})

	t.Run("a group takes no attempt and no settle", func(t *testing.T) {
		id := newRunID("g-no-attempt")
		if err := applyAdvance(t, s, id, store.Advance{NewSteps: []store.AdvanceStep{groupStep("g")}}, at); err != nil {
			t.Fatal(err)
		}
		attempt := store.Advance{NewAttempts: []store.AdvanceAttempt{{Path: "g", Attempt: 1, NewExecution: *execSpec("default")}}}
		if err := applyAdvance(t, s, id, attempt, at); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("attempt on a group = %v; want ErrConflict", err)
		}
		settle := store.Advance{Settle: []store.AdvanceSettle{{Path: "g", Attempt: 1}}}
		if err := applyAdvance(t, s, id, settle, at); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("settle of a group = %v; want ErrConflict", err)
		}
	})

	t.Run("invalid group change sets are rejected", func(t *testing.T) {
		bad := []store.Advance{
			{NewSteps: []store.AdvanceStep{{Path: "g", Kind: store.KindGroup, Execution: execSpec("default")}}},
			{NewSteps: []store.AdvanceStep{{Path: "g", Kind: store.KindGroup, Plugin: "shell"}}},
			{NewSteps: []store.AdvanceStep{{Path: "g", Kind: store.KindGroup, Status: store.StepSucceeded}}},
			{FinishGroups: []store.AdvanceFinishGroup{{Path: "g", Status: store.StepPending}}},
			{FinishGroups: []store.AdvanceFinishGroup{{Path: "", Status: store.StepSucceeded}}},
			{FinishGroups: []store.AdvanceFinishGroup{{Path: "g", Status: store.StepSucceeded}, {Path: "g", Status: store.StepFailed}}},
		}
		for i, a := range bad {
			if err := store.ValidateAdvance(a); err == nil {
				t.Errorf("case %d: ValidateAdvance accepted %+v", i, a)
			}
		}
	})

	t.Run("a stale version writes nothing", func(t *testing.T) {
		id := newRunID("g-stale")
		ver := runVersion(t, s, id)
		err := withTx(t, s, func(tx store.Tx) error {
			_, err := tx.ApplyAdvance(ctx, id, ver+5, store.Advance{NewSteps: []store.AdvanceStep{groupStep("g")}}, at)
			return err
		})
		if !errors.Is(err, store.ErrStale) {
			t.Fatalf("stale advance = %v; want ErrStale", err)
		}
		tree, _ := s.GetRunTree(ctx, id)
		for _, n := range tree.Steps {
			if n.Path == "g" {
				t.Fatal("a stale advance created the group")
			}
		}
	})
}
