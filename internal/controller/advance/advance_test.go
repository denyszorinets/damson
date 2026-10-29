package advance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/store"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

const linearSpec = `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"shell","with":{"cmd":"one"}},
{"id":"s2","plugin":"shell","with":{"cmd":"two"},"runs_on":{"pool":"gpu"}}]}}`

// fakeStore serves one run tree and records what the advance applies.
type fakeStore struct {
	mu        sync.Mutex
	trees     []store.RunTree // served in order; the last one repeats
	loads     int
	applyErrs []error // returned by successive ApplyAdvance calls
	applies   []store.Advance
	applyVers []int64
	enqueues  int
	spec      string
}

func (f *fakeStore) GetRunTree(context.Context, string) (store.RunTree, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := min(f.loads, len(f.trees)-1)
	f.loads++
	return f.trees[i], nil
}

func (f *fakeStore) GetJob(context.Context, string) (store.Job, error) {
	return store.Job{ID: "job", ProjectID: "p1", Name: "lin"}, nil
}

func (f *fakeStore) GetJobVersion(context.Context, string) (store.JobVersion, error) {
	return store.JobVersion{ID: "jv", Version: 3, Spec: json.RawMessage(f.spec)}, nil
}

func (f *fakeStore) ListProjects(context.Context) ([]store.Project, error) {
	return []store.Project{{ID: "p1", Name: "default"}}, nil
}

func (f *fakeStore) WithTx(ctx context.Context, fn func(store.Tx) error) error {
	return fn(&fakeTx{f: f})
}

type fakeTx struct{ f *fakeStore }

func (t *fakeTx) ApplyAdvance(_ context.Context, _ string, v int64, a store.Advance, _ time.Time) (store.Run, error) {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.f.applies = append(t.f.applies, a)
	t.f.applyVers = append(t.f.applyVers, v)
	if n := len(t.f.applies) - 1; n < len(t.f.applyErrs) && t.f.applyErrs[n] != nil {
		return store.Run{}, t.f.applyErrs[n]
	}
	return store.Run{ID: "r1", Version: v + 1}, nil
}

func (t *fakeTx) CreateRun(context.Context, store.NewRun, time.Time) (store.RunTree, error) {
	return store.RunTree{}, errors.New("unused")
}
func (t *fakeTx) RequestCancel(context.Context, string, time.Time) (store.Run, error) {
	return store.Run{}, errors.New("unused")
}
func (t *fakeTx) GetRun(context.Context, string) (store.Run, error) { return store.Run{}, nil }
func (t *fakeTx) TouchRun(context.Context, string) (int64, error)   { return 0, nil }
func (t *fakeTx) CompleteExecution(context.Context, store.Completion, time.Time) (store.Execution, error) {
	return store.Execution{}, nil
}
func (t *fakeTx) ExpireLeases(context.Context, time.Time, int) ([]store.Execution, error) {
	return nil, nil
}
func (t *fakeTx) Native() any { return nil }

func newEngine(t *testing.T) *engine.Engine {
	t.Helper()
	e, err := engine.New(expr.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func newCore(t *testing.T, f *fakeStore) (*core, *[]string) {
	t.Helper()
	if f.spec == "" {
		f.spec = linearSpec
	}
	var notified []string
	return &core{
		st: f, eng: newEngine(t), now: func() time.Time { return t0 },
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		enqueue: func(context.Context, store.Tx, string) error { f.enqueues++; return nil },
		notify:  func(id string) { notified = append(notified, id) },
	}, &notified
}

type stepSpec struct {
	path   string
	status store.StepStatus
	execs  []store.ExecutionState
	errMsg string
}

func tree(version int64, status store.RunStatus, cancel bool, steps ...stepSpec) store.RunTree {
	tr := store.RunTree{Run: store.Run{ID: "r1", JobID: "job", JobVersionID: "jv", Status: status, Version: version,
		Options: json.RawMessage(`{}`), Trigger: json.RawMessage(`{"type":"manual"}`)}}
	if status != store.RunPending {
		tr.StartedAt = &t0
	}
	if cancel {
		tr.CancelRequestedAt = &t0
	}
	for i, s := range steps {
		n := store.StepNode{StepInstance: store.StepInstance{
			ID: "si-" + s.path, RunID: "r1", Path: s.path, Kind: store.KindStep, Status: s.status,
			Outputs: json.RawMessage(`{}`), Error: s.errMsg, CreatedAt: t0.Add(time.Duration(i) * time.Second),
		}}
		for j, st := range s.execs {
			n.Executions = append(n.Executions, store.Execution{ID: "ex-" + s.path + string(rune('a'+j)), AttemptID: "at-" + s.path, State: st})
		}
		tr.Steps = append(tr.Steps, n)
	}
	return tr
}

func TestAdvanceDecisions(t *testing.T) {
	failedErr := `{"kind":"exit","message":"boom"}`
	tests := []struct {
		name  string
		tree  store.RunTree
		check func(t *testing.T, adv store.Advance)
		// wantApply is false when the run must not be written at all.
		wantApply   bool
		wantEnqueue int
	}{
		{
			name: "new run starts and schedules the first step", tree: tree(1, store.RunPending, false), wantApply: true,
			check: func(t *testing.T, adv store.Advance) {
				if !adv.Start || len(adv.NewSteps) != 1 || adv.NewSteps[0].Path != "s1" || adv.NewSteps[0].Execution == nil {
					t.Fatalf("advance = %+v", adv)
				}
				ex := adv.NewSteps[0].Execution
				if ex.Pool != DefaultPool || ex.Priority != 0 || !ex.ScheduledAt.Equal(t0) {
					t.Errorf("execution = %+v", ex)
				}
				if string(adv.NewSteps[0].Input) != `{"cmd":"one"}` {
					t.Errorf("input = %s", adv.NewSteps[0].Input)
				}
			},
		},
		{
			name: "step one succeeded schedules step two in its pool",
			tree: tree(2, store.RunRunning, false, stepSpec{"s1", store.StepSucceeded, []store.ExecutionState{store.ExecSucceeded}, ""}), wantApply: true,
			check: func(t *testing.T, adv store.Advance) {
				if adv.Start || adv.Finish != "" || len(adv.NewSteps) != 1 || adv.NewSteps[0].Path != "s2" || adv.NewSteps[0].Execution.Pool != "gpu" {
					t.Fatalf("advance = %+v", adv)
				}
			},
		},
		{
			name: "step one failed skips step two and fails the run",
			tree: tree(2, store.RunRunning, false, stepSpec{"s1", store.StepFailed, []store.ExecutionState{store.ExecFailed}, failedErr}), wantApply: true,
			check: func(t *testing.T, adv store.Advance) {
				if adv.Finish != store.RunFailed || len(adv.NewSteps) != 1 || adv.NewSteps[0].Status != store.StepSkipped || adv.NewSteps[0].Execution != nil {
					t.Fatalf("advance = %+v", adv)
				}
			},
		},
		{
			name: "timed out execution counts as a failed step",
			tree: tree(2, store.RunRunning, false, stepSpec{"s1", store.StepFailed, []store.ExecutionState{store.ExecTimedOut}, "timed out"}), wantApply: true,
			check: func(t *testing.T, adv store.Advance) {
				if adv.Finish != store.RunFailed {
					t.Fatalf("advance = %+v", adv)
				}
			},
		},
		{
			name: "all steps succeeded completes the run",
			tree: tree(3, store.RunRunning, false,
				stepSpec{"s1", store.StepSucceeded, []store.ExecutionState{store.ExecSucceeded}, ""},
				stepSpec{"s2", store.StepSucceeded, []store.ExecutionState{store.ExecSucceeded}, ""}), wantApply: true,
			check: func(t *testing.T, adv store.Advance) {
				if adv.Finish != store.RunSucceeded || len(adv.NewSteps) != 0 {
					t.Fatalf("advance = %+v", adv)
				}
			},
		},
		{
			name: "running execution means wait: no writes",
			tree: tree(2, store.RunRunning, false, stepSpec{"s1", store.StepPending, []store.ExecutionState{store.ExecRunning}, ""}),
		},
		{
			name: "queued execution means wait: no writes",
			tree: tree(2, store.RunRunning, false, stepSpec{"s1", store.StepPending, []store.ExecutionState{store.ExecQueued}, ""}),
		},
		{
			name: "cancel with a running execution writes nothing",
			tree: tree(3, store.RunRunning, true, stepSpec{"s1", store.StepPending, []store.ExecutionState{store.ExecRunning}, ""}),
		},
		{
			name:      "cancel with a queued execution cancels it and advances again",
			tree:      tree(3, store.RunRunning, true, stepSpec{"s1", store.StepPending, []store.ExecutionState{store.ExecQueued}, ""}),
			wantApply: true, wantEnqueue: 1,
			check: func(t *testing.T, adv store.Advance) {
				if len(adv.CancelQueued) != 1 || adv.CancelQueued[0] != "ex-s1a" || adv.Finish != "" {
					t.Fatalf("advance = %+v", adv)
				}
			},
		},
		{
			name: "cancelled step cancels the rest of the run",
			tree: tree(4, store.RunRunning, true, stepSpec{"s1", store.StepCanceled, []store.ExecutionState{store.ExecCanceled}, ""}), wantApply: true,
			check: func(t *testing.T, adv store.Advance) {
				if adv.Finish != store.RunCanceled || len(adv.NewSteps) != 1 || adv.NewSteps[0].Status != store.StepCanceled {
					t.Fatalf("advance = %+v", adv)
				}
			},
		},
		{
			name: "terminal run is left alone",
			tree: tree(9, store.RunSucceeded, false),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{trees: []store.RunTree{tt.tree}}
			c, notified := newCore(t, f)
			if err := c.advance(context.Background(), "r1"); err != nil {
				t.Fatalf("advance: %v", err)
			}
			if got := len(f.applies) > 0; got != tt.wantApply {
				t.Fatalf("applied = %v, want %v (%+v)", got, tt.wantApply, f.applies)
			}
			if f.enqueues != tt.wantEnqueue {
				t.Errorf("follow-up enqueues = %d, want %d", f.enqueues, tt.wantEnqueue)
			}
			if tt.wantApply {
				if len(f.applies) != 1 || f.applyVers[0] != tt.tree.Version {
					t.Errorf("applies = %d at versions %v, want one at %d", len(f.applies), f.applyVers, tt.tree.Version)
				}
				tt.check(t, f.applies[0])
			}
			if got := len(*notified) > 0; got != tt.wantApply {
				t.Errorf("notified = %v, want %v", *notified, tt.wantApply)
			}
		})
	}
}

func TestAdvanceVersionConflict(t *testing.T) {
	stale := store.ErrStale
	t.Run("retries on a reloaded tree", func(t *testing.T) {
		// Version 1 loses; the reload sees another controller's work (step one
		// is running), so the retry has nothing left to do.
		f := &fakeStore{
			trees:     []store.RunTree{tree(1, store.RunPending, false), tree(2, store.RunRunning, false, stepSpec{"s1", store.StepPending, []store.ExecutionState{store.ExecQueued}, ""})},
			applyErrs: []error{stale},
		}
		c, notified := newCore(t, f)
		if err := c.advance(context.Background(), "r1"); err != nil {
			t.Fatalf("advance: %v", err)
		}
		if f.loads != 2 || len(f.applies) != 1 || len(*notified) != 0 {
			t.Fatalf("loads=%d applies=%d notified=%v", f.loads, len(f.applies), *notified)
		}
	})
	t.Run("loses twice then wins", func(t *testing.T) {
		f := &fakeStore{
			trees:     []store.RunTree{tree(1, store.RunPending, false), tree(2, store.RunPending, false), tree(3, store.RunPending, false)},
			applyErrs: []error{stale, stale},
		}
		c, notified := newCore(t, f)
		if err := c.advance(context.Background(), "r1"); err != nil {
			t.Fatalf("advance: %v", err)
		}
		if len(f.applies) != 3 || f.applyVers[2] != 3 || len(*notified) != 1 {
			t.Fatalf("applies=%d versions=%v notified=%v", len(f.applies), f.applyVers, *notified)
		}
	})
	t.Run("gives up after repeated conflicts", func(t *testing.T) {
		errs := make([]error, 20)
		for i := range errs {
			errs[i] = stale
		}
		f := &fakeStore{trees: []store.RunTree{tree(1, store.RunPending, false)}, applyErrs: errs}
		c, _ := newCore(t, f)
		if err := c.advance(context.Background(), "r1"); !errors.Is(err, ErrConflicts) {
			t.Fatalf("advance = %v, want ErrConflicts", err)
		}
		if len(f.applies) != maxConflicts {
			t.Errorf("applies = %d, want %d", len(f.applies), maxConflicts)
		}
	})
	t.Run("other errors are returned without retry", func(t *testing.T) {
		boom := errors.New("disk on fire")
		f := &fakeStore{trees: []store.RunTree{tree(1, store.RunPending, false)}, applyErrs: []error{boom}}
		c, _ := newCore(t, f)
		if err := c.advance(context.Background(), "r1"); !errors.Is(err, boom) || len(f.applies) != 1 {
			t.Fatalf("advance = %v after %d applies", err, len(f.applies))
		}
	})
}

func TestAdvanceBadInputIsPermanent(t *testing.T) {
	for name, f := range map[string]*fakeStore{
		"unparsable spec":  {spec: `{"nope":1}`, trees: []store.RunTree{tree(1, store.RunPending, false)}},
		"step not in spec": {trees: []store.RunTree{tree(2, store.RunRunning, false, stepSpec{"ghost", store.StepPending, nil, ""})}},
		"corrupt run options": {trees: []store.RunTree{func() store.RunTree {
			tr := tree(1, store.RunPending, false)
			tr.Options = json.RawMessage(`[1`)
			return tr
		}()}},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := newCore(t, f)
			err := c.advance(context.Background(), "r1")
			var perm *permanentError
			if !errors.As(err, &perm) {
				t.Fatalf("advance = %v, want a permanent error", err)
			}
			if len(f.applies) != 0 {
				t.Errorf("applied %d times", len(f.applies))
			}
		})
	}
}

func TestStepErrorRoundTrip(t *testing.T) {
	if got := decodeStepError(`{"kind":"expression","message":"bad"}`); got.Kind != "expression" || got.Message != "bad" {
		t.Errorf("json error = %+v", got)
	}
	if got := decodeStepError("plain text"); got.Kind != "error" || got.Message != "plain text" {
		t.Errorf("text error = %+v", got)
	}
}
