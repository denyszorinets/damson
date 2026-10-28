package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/expr"
)

type decideFixture struct {
	JobFile string          `json:"job_file"`
	Now     time.Time       `json:"now"`
	State   json.RawMessage `json:"state"`
}

func newEngine(t *testing.T, l expr.Limits) *Engine {
	t.Helper()
	e, err := New(l)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func loadFixture(t *testing.T, path string) (*Job, RunState, time.Time) {
	t.Helper()
	var f decideFixture
	if err := json.Unmarshal(mustRead(t, path), &f); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	job, err := Parse(readFixture(t, f.JobFile))
	if err != nil {
		t.Fatalf("%s: %v", f.JobFile, err)
	}
	var st RunState
	dec := json.NewDecoder(bytes.NewReader(f.State))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		t.Fatalf("%s state: %v", path, err)
	}
	return job, st, f.Now
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestDecideGolden runs every testdata/engine/decide-*.input.json and compares
// the actions with the sibling .golden.json. Refresh with -update.
func TestDecideGolden(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join(testdata, "decide-*.input.json"))
	if err != nil || len(inputs) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	eng := newEngine(t, expr.Limits{})
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".input.json")
		t.Run(name, func(t *testing.T) {
			job, st, now := loadFixture(t, in)
			acts, err := eng.Decide(job, st, now)
			if err != nil {
				t.Fatal(err)
			}
			got, err := MarshalActions(acts)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := strings.TrimSuffix(in, ".input.json") + ".golden.json"
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if want := mustRead(t, golden); !bytes.Equal(got, want) {
				t.Fatalf("actions differ from %s (run with -update to refresh)\ngot:\n%s\nwant:\n%s", golden, got, want)
			}
		})
	}
}

// TestDecideDeterministicAndPure: same input gives the same actions, and the
// arguments are not modified.
func TestDecideDeterministicAndPure(t *testing.T) {
	inputs, _ := filepath.Glob(filepath.Join(testdata, "decide-*.input.json"))
	eng := newEngine(t, expr.Limits{})
	for _, in := range inputs {
		job, st, now := loadFixture(t, in)
		jobBefore, _ := json.Marshal(job)
		stBefore, _ := json.Marshal(st)
		var first []byte
		for range 5 {
			acts, err := eng.Decide(job, st, now)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := MarshalActions(acts)
			if first == nil {
				first = b
			} else if !bytes.Equal(first, b) {
				t.Fatalf("%s: non-deterministic result", in)
			}
		}
		jobAfter, _ := json.Marshal(job)
		stAfter, _ := json.Marshal(st)
		if !bytes.Equal(jobBefore, jobAfter) || !bytes.Equal(stBefore, stAfter) {
			t.Fatalf("%s: Decide mutated its arguments", in)
		}
	}
}

func jobFromSteps(t *testing.T, stepsJSON string) *Job {
	t.Helper()
	doc := `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"p","name":"j"},"spec":{"steps":` + stepsJSON + `}}`
	j, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func actionTypes(acts []Action) []string {
	out := make([]string, len(acts))
	for i, a := range acts {
		out[i] = a.ActionType()
	}
	return out
}

func TestDecideRules(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	now := t0.Add(time.Minute)
	done := func(id string, st StepStatus) StepInstanceState {
		return StepInstanceState{Path: id, StepID: id, Status: st, CreatedAt: t0}
	}
	const abc = `[{"id":"a","plugin":"x"},{"id":"b","plugin":"x"},{"id":"c","plugin":"x"}]`
	const coe = `[{"id":"a","plugin":"x","continue_on_error":true},{"id":"b","plugin":"x"}]`
	const status = `[{"id":"a","plugin":"x"},
		{"id":"b","plugin":"x","when":"steps.a.status == 'failed'"},
		{"id":"c","plugin":"x","when":"steps.a.status == 'failed' && failure()"}]`
	tests := []struct {
		name  string
		steps string
		state []StepInstanceState
		want  []string
		last  RunStatus // expected CompleteRun status, if any
	}{
		{"rule 1: first step starts alone", abc, nil, []string{"create_step_instance", "schedule_execution"}, ""},
		{"rule 1: sequential, next after success", abc, []StepInstanceState{done("a", StepSucceeded)},
			[]string{"create_step_instance", "schedule_execution"}, ""},
		{"rule 2: false when skips", `[{"id":"a","plugin":"x","when":"false"}]`, nil,
			[]string{"skip_step", "complete_run"}, RunSucceeded},
		{"rule 3: failure skips rest", abc, []StepInstanceState{done("a", StepFailed)},
			[]string{"skip_step", "skip_step", "complete_run"}, RunFailed},
		{"rule 3: continue_on_error does not skip", coe, []StepInstanceState{done("a", StepFailed)},
			[]string{"create_step_instance", "schedule_execution"}, ""},
		{"rule 3: continue_on_error run succeeds", coe, []StepInstanceState{done("a", StepFailed), done("b", StepSucceeded)},
			[]string{"complete_run"}, RunSucceeded},
		{"rule 3: failed without continue_on_error fails run", coe, []StepInstanceState{done("a", StepSucceeded), done("b", StepFailed)},
			[]string{"complete_run"}, RunFailed},
		{"explicit status expr without status function still gated by success", status,
			[]StepInstanceState{done("a", StepFailed)},
			[]string{"skip_step", "create_step_instance", "schedule_execution"}, ""},
		{"skipped steps do not fail the run", abc,
			[]StepInstanceState{done("a", StepSucceeded), done("b", StepSkipped), done("c", StepSucceeded)},
			[]string{"complete_run"}, RunSucceeded},
		{"waits while a step is in flight", abc,
			[]StepInstanceState{{Path: "a", StepID: "a", Status: StepPending, CreatedAt: t0,
				Executions: []ExecutionView{{ID: "e", AttemptID: "t", State: ExecRunning}}}},
			nil, ""},
		{"repairs an instance without execution", abc,
			[]StepInstanceState{{Path: "a", StepID: "a", Status: StepPending, CreatedAt: t0}},
			[]string{"schedule_execution"}, ""},
	}
	eng := newEngine(t, expr.Limits{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := jobFromSteps(t, tt.steps)
			st := RunState{ID: "r", Status: RunRunning, StartedAt: &t0, Steps: tt.state}
			acts, err := eng.Decide(job, st, now)
			if err != nil {
				t.Fatal(err)
			}
			got := actionTypes(acts)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("actions = %v, want %v", got, tt.want)
			}
			if tt.last != "" {
				cr := acts[len(acts)-1].(CompleteRun)
				if cr.Status != tt.last || !cr.FinishedAt.Equal(now) {
					t.Fatalf("CompleteRun = %+v, want %s at %s", cr, tt.last, now)
				}
			}
		})
	}
}

func TestDecideStepTimeoutIsData(t *testing.T) {
	job := jobFromSteps(t, `[{"id":"a","plugin":"x","timeout":"90s"}]`)
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	acts, err := newEngine(t, expr.Limits{}).Decide(job, RunState{ID: "r", Status: RunRunning}, now)
	if err != nil {
		t.Fatal(err)
	}
	se := acts[1].(ScheduleExecution)
	if time.Duration(se.Timeout) != 90*time.Second || se.Attempt != 1 || !se.ScheduledAt.Equal(now) {
		t.Fatalf("schedule = %+v", se)
	}
}

func TestDecideExpressionErrors(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		steps  string
		limits expr.Limits
		want   string
	}{
		{"when eval error", `[{"id":"a","plugin":"x","when":"inputs.missing == 1"}]`, expr.Limits{}, "evaluating when"},
		{"when not bool", `[{"id":"a","plugin":"x","when":"inputs.n"}]`, expr.Limits{}, "want bool"},
		{"with eval error", `[{"id":"a","plugin":"x","with":{"k":"${{ steps.nope.outputs.x }}"}}]`, expr.Limits{}, "resolving input"},
		{"cost limit", `[{"id":"a","plugin":"x","with":{"k":"${{ inputs.big.map(x, x + 1).size() }}"}}]`,
			expr.Limits{CostLimit: 20}, "cost limit"},
		{"result size limit", `[{"id":"a","plugin":"x","with":{"k":"${{ inputs.big }}"}}]`,
			expr.Limits{MaxResultBytes: 16}, "too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			big := make([]any, 200)
			for i := range big {
				big[i] = float64(i)
			}
			st := RunState{ID: "r", Status: RunRunning, Inputs: map[string]any{"n": float64(1), "big": big}}
			acts, err := newEngine(t, tt.limits).Decide(jobFromSteps(t, tt.steps), st, now)
			if err != nil {
				t.Fatal(err)
			}
			if got := actionTypes(acts); strings.Join(got, ",") != "fail_step,complete_run" {
				t.Fatalf("actions = %v", got)
			}
			fs := acts[0].(FailStep)
			if fs.Error.Kind != ErrKindExpression || !strings.Contains(fs.Error.Message, tt.want) {
				t.Fatalf("error = %+v, want %q", fs.Error, tt.want)
			}
			if acts[1].(CompleteRun).Status != RunFailed {
				t.Fatalf("run = %v", acts[1])
			}
		})
	}
}

func TestDecideInconsistentState(t *testing.T) {
	job := jobFromSteps(t, `[{"id":"a","plugin":"x"}]`)
	eng := newEngine(t, expr.Limits{})
	now := time.Now()
	tests := map[string][]StepInstanceState{
		"unknown step": {{Path: "zzz", StepID: "zzz", Status: StepSucceeded}},
		"duplicate":    {{Path: "a", StepID: "a", Status: StepSucceeded}, {Path: "a2", StepID: "a", Status: StepSucceeded}},
	}
	for name, steps := range tests {
		if _, err := eng.Decide(job, RunState{ID: "r", Status: RunRunning, Steps: steps}, now); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

// Secret references pass through resolution untouched and no secret value is
// ever available to expressions (§5.4, hard rule 5).
func TestResolveInputSecretRefsStayLiteral(t *testing.T) {
	eng := newEngine(t, expr.Limits{})
	step := Step{ID: "a", Plugin: "x", With: map[string]any{
		"conn": "secret://infra/s3", "url": "s3://${{ inputs.bucket }}/k", "n": "${{ 1 + 2 }}", "html": "<a&b>",
	}}
	raw, err := eng.ResolveInput(step, expr.Vars{Inputs: map[string]any{"bucket": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"conn":"secret://infra/s3","html":"<a&b>","n":3,"url":"s3://b/k"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
	if _, err := eng.ResolveInput(Step{With: map[string]any{"k": "${{ secrets.token }}"}}, expr.Vars{}); err == nil {
		t.Fatal("secrets must not be addressable from expressions")
	}
}
