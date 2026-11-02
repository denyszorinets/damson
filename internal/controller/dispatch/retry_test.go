package dispatch_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// flakyPlugin fails with a connection error for the first `fail` calls and then
// succeeds. State lives on the instance, one per test.
type flakyPlugin struct {
	mu    sync.Mutex
	fail  int
	calls int
}

func (p *flakyPlugin) Manifest() pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: "flaky", Version: "1.0.0", Title: "flaky", TargetMode: pluginsdk.TargetNone,
		InputSchema: pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
	}
}

func (p *flakyPlugin) Validate(context.Context, json.RawMessage) error { return nil }

func (p *flakyPlugin) Execute(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n <= p.fail {
		return pluginsdk.Result{Error: pluginsdk.NewError(pluginsdk.KindConnection, "connection reset", nil)}, nil
	}
	return pluginsdk.Result{Outputs: json.RawMessage(`{}`)}, nil
}

func (p *flakyPlugin) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func retrySpec(maxAttempts int) string {
	return `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"flaky","retry":{"max_attempts":` + string(rune('0'+maxAttempts)) + `,"backoff":{"initial":"50ms","multiplier":2,"max":"1s","jitter":0.2}}},
{"id":"s2","plugin":"demo","with":{"word":"after"}}]}}`
}

// A step that fails twice with a retryable error and then succeeds is run three
// times, as three attempts of one step, with a pause between them.
func TestRetryFlakyStepSucceedsOnThirdAttempt(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		fp := &flakyPlugin{fail: 2}
		c := startClusterPlugins(t, st, retrySpec(5), fp)
		id := c.triggerPlugin("flaky", "x")

		tree := c.waitRun(id, store.RunSucceeded)
		s1 := tree.Steps[0]
		if s1.Status != store.StepSucceeded || len(s1.Executions) != 3 {
			t.Fatalf("s1 = %s with %d attempts; want succeeded after 3", s1.Status, len(s1.Executions))
		}
		ids := map[string]bool{}
		for i, ex := range s1.Executions {
			if ex.AttemptNo != i+1 || ids[ex.AttemptID] {
				t.Fatalf("attempt %d = %+v; want consecutive numbers and distinct attempt ids", i+1, ex)
			}
			ids[ex.AttemptID] = true
		}
		if s1.Executions[0].State != store.ExecFailed || s1.Executions[1].State != store.ExecFailed || s1.Executions[2].State != store.ExecSucceeded {
			t.Fatalf("attempt states = %v %v %v", s1.Executions[0].State, s1.Executions[1].State, s1.Executions[2].State)
		}
		// The wait before a retry is at least (1-jitter) of the backoff: 40ms, then 80ms.
		for i, minGap := range []time.Duration{40 * time.Millisecond, 80 * time.Millisecond} {
			gap := s1.Executions[i+1].ScheduledAt.Sub(*s1.Executions[i].FinishedAt)
			if gap < minGap-time.Millisecond {
				t.Fatalf("retry %d scheduled %v after the failure; want at least %v", i+1, gap, minGap)
			}
		}
		if fp.callCount() != 3 {
			t.Fatalf("plugin ran %d times; want 3", fp.callCount())
		}
		if s2 := tree.Steps[1]; s2.Status != store.StepSucceeded {
			t.Fatalf("s2 = %s; want it to run after the retries", s2.Status)
		}
	})
}

// When every attempt fails, the step fails after max_attempts with the last
// attempt's error and the run fails.
func TestRetryExhaustedFailsStep(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		fp := &flakyPlugin{fail: 100}
		c := startClusterPlugins(t, st, retrySpec(3), fp)
		id := c.triggerPlugin("flaky", "x")

		tree := c.waitRun(id, store.RunFailed)
		s1 := tree.Steps[0]
		if s1.Status != store.StepFailed || len(s1.Executions) != 3 {
			t.Fatalf("s1 = %s with %d attempts; want failed after 3", s1.Status, len(s1.Executions))
		}
		if kind, _ := store.SplitStepError(s1.Error); kind != "connection" {
			t.Fatalf("step error = %q; want the last attempt's connection error", s1.Error)
		}
		if fp.callCount() != 3 {
			t.Fatalf("plugin ran %d times; want exactly max_attempts", fp.callCount())
		}
		for _, n := range tree.Steps[1:] {
			if len(n.Executions) != 0 {
				t.Fatalf("step %s ran after the failure: %+v", n.Path, n.Executions)
			}
		}
	})
}

// A cancel during the wait before a retry cancels the queued attempt: the step
// is canceled, not retried.
func TestCancelDuringRetryWait(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		spec := `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"flaky","retry":{"max_attempts":5,"backoff":{"initial":"30s","jitter":0}}}]}}`
		fp := &flakyPlugin{fail: 100}
		c := startClusterPlugins(t, st, spec, fp)
		id := c.triggerPlugin("flaky", "x")
		deadline := time.Now().Add(20 * time.Second)
		for {
			tree, err := st.GetRunTree(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if n := tree.Steps[0].Executions; len(n) == 2 && n[1].State == store.ExecQueued {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the retry was never queued: %+v", tree.Steps[0].Executions)
			}
			time.Sleep(20 * time.Millisecond)
		}
		c.requestCancel(id)
		tree := c.waitRun(id, store.RunCanceled)
		if s1 := tree.Steps[0]; s1.Status != store.StepCanceled || len(s1.Executions) != 2 || s1.Executions[1].State != store.ExecCanceled {
			t.Fatalf("s1 = %s %+v; want canceled with the queued retry canceled", s1.Status, s1.Executions)
		}
		if fp.callCount() != 1 {
			t.Fatalf("plugin ran %d times; want only the first attempt", fp.callCount())
		}
	})
}
