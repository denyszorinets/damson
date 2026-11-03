package advance

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/store"
)

func TestToAdvanceGroups(t *testing.T) {
	job := &engine.Job{Spec: engine.Spec{Steps: []engine.Step{{ID: "g", Type: engine.StepTypeGroup, Steps: []engine.Step{
		{ID: "a", Plugin: "ssh", RunsOn: &engine.RunsOn{Pool: "gpu"}},
	}}}}}
	acts := []engine.Action{
		engine.CreateStepInstance{Path: "g", StepID: "g", Kind: "group", CreatedAt: t0},
		engine.CreateStepInstance{Path: "g/a", StepID: "a", Plugin: "ssh", Input: json.RawMessage(`{}`), CreatedAt: t0},
		engine.ScheduleExecution{Path: "g/a", Attempt: 1, ScheduledAt: t0},
		engine.SkipStep{Path: "g/b", StepID: "b", Status: engine.StepSkipped, Reason: "x"},
		engine.SkipStep{Path: "h", StepID: "h", Kind: "group", Status: engine.StepSkipped, Reason: "when condition is false"},
		engine.CompleteGroup{Path: "g", Status: engine.StepFailed, Error: &engine.StepError{Kind: "exit_code", Message: "boom", Path: "g/a"}},
		engine.CompleteGroup{Path: "ok", Status: engine.StepSucceeded},
	}
	adv, _, err := toAdvance(acts, store.RunTree{}, job)
	if err != nil {
		t.Fatal(err)
	}
	if len(adv.NewSteps) != 4 {
		t.Fatalf("NewSteps = %+v", adv.NewSteps)
	}
	if g := adv.NewSteps[0]; g.Kind != store.KindGroup || g.Path != "g" || g.Execution != nil {
		t.Fatalf("group step = %+v; want a group with no execution", g)
	}
	if a := adv.NewSteps[1]; a.Path != "g/a" || a.Execution == nil || a.Execution.Pool != "gpu" {
		t.Fatalf("nested step = %+v; want the pool of its spec step (gpu), found by path", a)
	}
	if h := adv.NewSteps[3]; h.Kind != store.KindGroup || h.Status != store.StepSkipped {
		t.Fatalf("skipped group = %+v", h)
	}
	if len(adv.FinishGroups) != 2 {
		t.Fatalf("FinishGroups = %+v", adv.FinishGroups)
	}
	failed := adv.FinishGroups[0]
	if failed.Path != "g" || failed.Status != store.StepFailed {
		t.Fatalf("finish = %+v", failed)
	}
	if kind, msg := store.SplitStepError(failed.Error); kind != "exit_code" || msg != "boom" {
		t.Fatalf("finish error = %q", failed.Error)
	}
	if ok := adv.FinishGroups[1]; ok.Status != store.StepSucceeded || ok.Error != "" {
		t.Fatalf("finish = %+v", ok)
	}
	if err := store.ValidateAdvance(adv); err != nil {
		t.Fatalf("the store rejects the converted change set: %v", err)
	}
}

func TestBuildStateKeepsGroups(t *testing.T) {
	tree := store.RunTree{Run: store.Run{ID: "r", Status: store.RunRunning}}
	tree.Steps = []store.StepNode{
		{StepInstance: store.StepInstance{Path: "g", Kind: store.KindGroup, Status: store.StepRunning, Outputs: []byte("{}")}},
		{StepInstance: store.StepInstance{Path: "g/a", Kind: store.KindStep, Status: store.StepFailed, Outputs: []byte("{}"),
			Error: store.StepErrorText("exit_code", "boom"), CreatedAt: time.Time{}}},
		{StepInstance: store.StepInstance{Path: "bad", Kind: store.KindGroup, Status: store.StepFailed, Outputs: []byte("{}"),
			Error: `{"kind":"exit_code","message":"boom","path":"bad/x"}`}},
	}
	st, err := buildState(tree, jobMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Steps) != 3 {
		t.Fatalf("steps = %d; groups must not be dropped", len(st.Steps))
	}
	if g := st.Steps[0]; g.Kind != "group" || g.StepID != "g" || g.Status != engine.StepPending {
		t.Fatalf("group = %+v; want a pending group", g)
	}
	if a := st.Steps[1]; a.StepID != "a" || a.Kind != "" {
		t.Fatalf("nested step = %+v; want id a (last segment) and kind step", a)
	}
	if bad := st.Steps[2]; bad.Status != engine.StepFailed || bad.Error == nil || bad.Error.Path != "bad/x" {
		t.Fatalf("failed group = %+v; want its error with the failing path", bad)
	}
}

func TestLostPluginRefsFindNestedSteps(t *testing.T) {
	job := &engine.Job{Spec: engine.Spec{Steps: []engine.Step{{ID: "g", Type: engine.StepTypeGroup, Steps: []engine.Step{{ID: "a", Plugin: "tool@2"}}}}}}
	state := engine.RunState{Steps: []engine.StepInstanceState{
		{Path: "g", StepID: "g", Kind: "group", Status: engine.StepPending},
		{Path: "g/a", StepID: "a", Status: engine.StepPending, Executions: []engine.ExecutionView{{ID: "e", Attempt: 1, State: engine.ExecLost}}},
	}}
	refs := lostPluginRefs(state, job)
	if len(refs) != 1 || refs[0] != "tool@2" {
		t.Fatalf("refs = %v; want the nested step's plugin", refs)
	}
}

func TestToAdvanceCollectsEveryWake(t *testing.T) {
	deadline := t0.Add(time.Hour)
	grace := t0.Add(2 * time.Minute)
	acts := []engine.Action{
		engine.ScheduleWake{At: deadline},
		engine.ScheduleWake{At: grace},
		engine.CompleteGroup{Path: "g", Status: engine.StepSucceeded, Handled: true, Error: &engine.StepError{Kind: "exit_code", Message: "boom", Path: "g/a"}},
	}
	adv, wakes, err := toAdvance(acts, store.RunTree{}, &engine.Job{})
	if err != nil {
		t.Fatal(err)
	}
	if len(wakes) != 2 || !wakes[0].Equal(deadline) || !wakes[1].Equal(grace) {
		t.Fatalf("wakes = %v; want both, in order", wakes)
	}
	// A handled group is a succeeded group that keeps the caught error.
	if len(adv.FinishGroups) != 1 || adv.FinishGroups[0].Status != store.StepSucceeded || adv.FinishGroups[0].Error == "" {
		t.Fatalf("FinishGroups = %+v; want a succeeded group with its error kept", adv.FinishGroups)
	}
}

func TestBuildStateMarksHandledGroups(t *testing.T) {
	tree := store.RunTree{Run: store.Run{ID: "r", Status: store.RunRunning}}
	tree.Steps = []store.StepNode{
		{StepInstance: store.StepInstance{Path: "caught", Kind: store.KindGroup, Status: store.StepSucceeded, Outputs: []byte("{}"),
			Error: `{"kind":"exit_code","message":"boom","path":"caught/a"}`}},
		{StepInstance: store.StepInstance{Path: "clean", Kind: store.KindGroup, Status: store.StepSucceeded, Outputs: []byte("{}")}},
		{StepInstance: store.StepInstance{Path: "plain", Kind: store.KindStep, Status: store.StepSucceeded, Outputs: []byte("{}")}},
	}
	st, err := buildState(tree, jobMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if c := st.Steps[0]; !c.Handled || c.Error == nil || c.Error.Path != "caught/a" {
		t.Fatalf("caught group = %+v; want handled with its error and failing path", c)
	}
	if st.Steps[1].Handled || st.Steps[2].Handled {
		t.Fatal("a group without a kept error, or a step, was marked handled")
	}
}
