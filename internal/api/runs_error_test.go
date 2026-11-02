package api

import (
	"testing"

	"github.com/denyszorinets/damson/internal/store"
)

func TestExecutionAndStepErrorFields(t *testing.T) {
	timedOut := store.Execution{ID: "e1", State: store.ExecTimedOut, Error: store.StepErrorText("timeout", "step timed out")}
	got := toExecution(timedOut)
	if got.ErrorKind != "timeout" || got.Error != "step timed out" {
		t.Fatalf("execution = kind %q error %q; want timeout, the message without the JSON", got.ErrorKind, got.Error)
	}

	lost := toExecution(store.Execution{ID: "e2", State: store.ExecLost, Error: "lease expired"})
	if lost.ErrorKind != "lost" || lost.Error != "lease expired" {
		t.Fatalf("lost execution = kind %q error %q", lost.ErrorKind, lost.Error)
	}

	if ok := toExecution(store.Execution{ID: "e3", State: store.ExecSucceeded}); ok.ErrorKind != "" || ok.Error != "" {
		t.Fatalf("succeeded execution has an error: %+v", ok)
	}
	if running := toExecution(store.Execution{ID: "e4", State: store.ExecRunning}); running.ErrorKind != "" {
		t.Fatalf("running execution has an error kind: %+v", running)
	}

	step := toStep(store.StepNode{StepInstance: store.StepInstance{ID: "s1", Path: "a", Status: store.StepFailed,
		Error: store.StepErrorText("timeout", "step timed out")}})
	if step.ErrorKind != "timeout" || step.Error != "step timed out" {
		t.Fatalf("step = kind %q error %q", step.ErrorKind, step.Error)
	}
	plain := toStep(store.StepNode{StepInstance: store.StepInstance{ID: "s2", Path: "b", Status: store.StepCanceled, Error: "run cancelled"}})
	if plain.ErrorKind != "" || plain.Error != "run cancelled" {
		t.Fatalf("plain step error = kind %q error %q", plain.ErrorKind, plain.Error)
	}
}
