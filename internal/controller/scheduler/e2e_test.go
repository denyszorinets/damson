package scheduler_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/controller/scheduler"
	"github.com/denyszorinets/damson/internal/store"
)

const jobSpec = `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"e2e"},
"spec":{"options":{"type":"object","properties":{"env":{"type":"string","enum":["dev","prod"],"default":"dev"}}},
"steps":[{"id":"a","plugin":"ssh","with":{"host":"h"}}]}}`

// A schedule fires through the real Starter: the run exists, carries the
// schedule trigger and the options, and the schedule has moved on in the same
// transaction.
func TestFiresThroughTheRealStarter(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	job, _, err := h.st.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "e2e", Spec: json.RawMessage(jobSpec), Checksum: "x"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	h.jobID = job.ID
	h.add(t, "real", func(s *store.Schedule) { s.Options = json.RawMessage(`{"env":"prod"}`) })
	h.add(t, "broken", func(s *store.Schedule) { s.Options = json.RawMessage(`{"env":"staging"}`) })

	now := t0.Add(time.Second)
	d, err := scheduler.New(scheduler.Config{
		Store: h.st, Starter: api.NewStarter(api.Config{Data: h.st}), Leader: h.el, Holder: "c1", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Once(ctx)
	d.Once(ctx)

	real, broken := h.get(t, "real"), h.get(t, "broken")
	if real.LastRunID == "" || real.LastError != "" {
		t.Fatalf("real = %+v", real)
	}
	run, err := h.st.GetRun(ctx, real.LastRunID)
	if err != nil {
		t.Fatal(err)
	}
	var trig api.Trigger
	if err := json.Unmarshal(run.Trigger, &trig); err != nil {
		t.Fatal(err)
	}
	if trig.Type != "schedule" || trig.ScheduleID != "real" || trig.FireAt != "2026-03-01T10:00:00Z" || string(run.Options) != `{"env":"prod"}` {
		t.Fatalf("run trigger = %+v, options = %s", trig, run.Options)
	}
	// An invalid schedule makes no run, says why without the value, and moves on.
	if broken.LastRunID != "" || broken.LastError == "" || !broken.NextFireAt.After(now) {
		t.Fatalf("broken = %+v", broken)
	}
	if contains(broken.LastError, "staging") {
		t.Fatalf("the error carries an option value: %q", broken.LastError)
	}
	page, err := h.st.QueryRuns(ctx, store.RunFilter{}, "", 10)
	if err != nil || len(page.Runs) != 1 {
		t.Fatalf("runs = %+v, %v; want exactly one", page, err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
