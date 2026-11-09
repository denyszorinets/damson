package agent

import (
	"context"
	"testing"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func TestDrainWaitsForRunningSteps(t *testing.T) {
	ctl := newFakeCtl(t)
	b := newBlocker()
	a, _ := startAgent(t, ctl, []pluginsdk.Plugin{plug(b.run)})
	ctl.mustExecute(stepReq("a1"))
	b.waitStarted(t)

	done := make(chan struct{})
	go func() {
		a.Drain(context.Background())
		close(done)
	}()
	eventually(t, "draining", a.Draining)
	select {
	case <-done:
		t.Fatal("Drain returned while a step was running")
	case <-time.After(150 * time.Millisecond):
	}
	if res, err := ctl.execute(stepReq("a2")); err == nil && res.Accepted {
		t.Error("a draining agent accepted new work")
	}
	close(b.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not return after the step finished")
	}
	if c := completeOf(t, ctl, "a1"); c.AttemptID != "a1" {
		t.Errorf("complete = %+v", c)
	}
}

func TestDrainGraceEndsWithRunningStep(t *testing.T) {
	ctl := newFakeCtl(t)
	b := newBlocker()
	a, _ := startAgent(t, ctl, []pluginsdk.Plugin{plug(b.run)})
	ctl.mustExecute(stepReq("a1"))
	b.waitStarted(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	a.Drain(ctx)
	if time.Since(start) > 3*time.Second {
		t.Error("Drain ignored its deadline")
	}
	if len(a.exec.Running()) != 1 {
		t.Error("Drain must leave canceling to Stop")
	}
}

func TestDrainSurvivesReconnect(t *testing.T) {
	ctl := newFakeCtl(t)
	a, _ := startAgent(t, ctl, nil)
	a.Drain(context.Background())
	if !a.Draining() {
		t.Fatal("not draining after Drain")
	}
	a.draining.Store(false) // what every hello does
	if !a.Draining() {
		t.Error("a new session cleared the local drain")
	}
}
