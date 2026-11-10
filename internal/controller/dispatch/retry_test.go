package dispatch_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// flakyPlugin fails with a connection error for the first `fail` calls and then
// succeeds. State lives on the instance, one per test.
type flakyPlugin struct {
	mu    sync.Mutex
	fail  int
	code  int // when set, the failures are exit_code errors with this code
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
	if n <= p.fail && p.code != 0 {
		return pluginsdk.Result{Error: pluginsdk.ExitCodeError(p.code, "script failed")}, nil
	}
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
		c := startClusterPlugins(t, st, retrySpec(5), []pluginsdk.Plugin{fp})
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
		c := startClusterPlugins(t, st, retrySpec(3), []pluginsdk.Plugin{fp})
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
		c := startClusterPlugins(t, st, spec, []pluginsdk.Plugin{fp})
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

// lostPlugin blocks its first call (the agent keeps running it while the
// controller loses its lease) and succeeds on later calls. Its manifest says
// whether it is idempotent.
type lostPlugin struct {
	idempotent bool
	release    chan struct{}

	mu    sync.Mutex
	calls int
}

func (p *lostPlugin) Manifest() pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: "lostp", Version: "1.0.0", Title: "lostp", TargetMode: pluginsdk.TargetNone, Idempotent: p.idempotent,
		InputSchema: pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
	}
}

func (p *lostPlugin) Validate(context.Context, json.RawMessage) error { return nil }

func (p *lostPlugin) Execute(ctx context.Context, _ pluginsdk.ExecRequest, _ pluginsdk.Emitter) (pluginsdk.Result, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n == 1 {
		select {
		case <-p.release:
		case <-ctx.Done():
		}
		return pluginsdk.Result{Error: &pluginsdk.StepError{Message: "stopped"}}, nil
	}
	return pluginsdk.Result{Outputs: json.RawMessage(`{}`)}, nil
}

// A lost attempt (its lease expires because nothing renews it) is retried when
// the plugin's manifest says it is idempotent, and fails the step with kind
// lost when it is not, with retry.on_lost unset in both cases.
func TestLostAttemptRetriedOnlyForIdempotentPlugin(t *testing.T) {
	spec := `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"lostp","retry":{"max_attempts":3,"backoff":{"initial":"20ms","jitter":0}}}]}}`
	for _, idempotent := range []bool{true, false} {
		name := map[bool]string{true: "idempotent", false: "not idempotent"}[idempotent]
		t.Run(name, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				lp := &lostPlugin{idempotent: idempotent, release: make(chan struct{})}
				t.Cleanup(func() { close(lp.release) })
				// A short lease that nothing renews: the reaper marks attempt 1 lost
				// while the agent still runs it.
				c := startClusterPlugins(t, st, spec, []pluginsdk.Plugin{lp}, func(cfg *dispatch.Config) {
					cfg.LeaseTTL = 300 * time.Millisecond
					cfg.RenewInterval = time.Hour
				})
				id := c.triggerPlugin("lostp", "x")

				if idempotent {
					tree := c.waitRun(id, store.RunSucceeded)
					s1 := tree.Steps[0]
					if len(s1.Executions) != 2 || s1.Executions[0].State != store.ExecLost || s1.Executions[1].State != store.ExecSucceeded {
						t.Fatalf("attempts = %+v; want lost then succeeded", s1.Executions)
					}
					return
				}
				tree := c.waitRun(id, store.RunFailed)
				s1 := tree.Steps[0]
				if len(s1.Executions) != 1 || s1.Executions[0].State != store.ExecLost || s1.Status != store.StepFailed {
					t.Fatalf("s1 = %s with %+v; want failed after one lost attempt", s1.Status, s1.Executions)
				}
				if kind, _ := store.SplitStepError(s1.Error); kind != store.ErrKindLost {
					t.Fatalf("step error = %q; want kind lost", s1.Error)
				}
			})
		})
	}
}

func exitRetrySpec(retryOn string) string {
	return `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"flaky","retry":{"max_attempts":4,"retry_on":"` + retryOn + `","backoff":{"initial":"20ms","multiplier":1,"max":"1s","jitter":0}}}]}}`
}

// error.exit_code reaches retry_on: exit 1 is retried until the plugin
// succeeds (ADR-0036, #95).
func TestRetryOnExitCode(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		fp := &flakyPlugin{fail: 2, code: 1}
		c := startClusterPlugins(t, st, exitRetrySpec("error.exit_code == 1"), []pluginsdk.Plugin{fp})
		tree := c.waitRun(c.triggerPlugin("flaky", "x"), store.RunSucceeded)
		if n := len(tree.Steps[0].Executions); n != 3 {
			t.Fatalf("exit 1 ran %d attempts; want 3", n)
		}
	})
}

// Exit 2 does not match the condition: no retry, and the code stays visible on
// the failed step.
func TestNoRetryOnOtherExitCode(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		fp := &flakyPlugin{fail: 100, code: 2}
		c := startClusterPlugins(t, st, exitRetrySpec("error.exit_code == 1"), []pluginsdk.Plugin{fp})
		tree := c.waitRun(c.triggerPlugin("flaky", "x"), store.RunFailed)
		s1 := tree.Steps[0]
		if len(s1.Executions) != 1 || s1.Status != store.StepFailed {
			t.Fatalf("exit 2 = %s with %d attempts; want failed after 1", s1.Status, len(s1.Executions))
		}
		var e struct {
			ExitCode *int `json:"exit_code"`
		}
		if err := json.Unmarshal([]byte(s1.Error), &e); err != nil || e.ExitCode == nil || *e.ExitCode != 2 {
			t.Fatalf("step error %q; want exit_code 2", s1.Error)
		}
	})
}
