package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

type raceResult struct {
	won, stale int
	other      []error
}

// raceN runs fn n times concurrently and tallies nil / ErrStale / other errors.
func raceN(n int, fn func(i int) error) raceResult {
	var (
		mu  sync.Mutex
		res raceResult
		wg  sync.WaitGroup
	)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := fn(i)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				res.won++
			case errors.Is(err, store.ErrStale):
				res.stale++
			default:
				res.other = append(res.other, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	return res
}

func execSpec(pool string) *store.NewExecution {
	return &store.NewExecution{Pool: pool, ScheduledAt: t0}
}

// mustRun creates a job and a run with one executable step "main/a".
func mustRun(t *testing.T, s store.Store, jobName string, steps ...store.NewStepInstance) (store.Job, store.RunTree) {
	t.Helper()
	job, ver := mustJob(t, s, jobName)
	if len(steps) == 0 {
		steps = []store.NewStepInstance{{Path: "main/a", Plugin: "shell", Input: json.RawMessage(`{"cmd":"true"}`), Execution: execSpec("default")}}
	}
	tree, err := s.CreateRun(context.Background(), store.NewRun{
		JobID: job.ID, JobVersionID: ver.ID, Trigger: json.RawMessage(`{"type":"manual"}`),
		Options: json.RawMessage(`{"env":"prod"}`), ConcurrencyKey: "k1", Steps: steps,
	}, t0)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return job, tree
}

func testCreateRun(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	job, tree := mustRun(t, s, "j",
		store.NewStepInstance{Path: "main", Kind: store.KindGroup},
		store.NewStepInstance{Path: "main/a", Plugin: "shell", Input: json.RawMessage(`{"cmd":"true"}`), Execution: execSpec("default")},
	)
	if tree.Status != store.RunPending || tree.Version != 1 || len(tree.Steps) != 2 {
		t.Fatalf("CreateRun = %+v", tree.Run)
	}
	if got := tree.Steps[1].Executions; len(got) != 1 || got[0].State != store.ExecQueued || got[0].AttemptNo != 1 || got[0].AttemptID == "" {
		t.Fatalf("first execution = %+v", got)
	}

	got, err := s.GetRunTree(ctx, tree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != tree.ID || got.Status != store.RunPending || got.ConcurrencyKey != "k1" || len(got.Steps) != 2 {
		t.Fatalf("GetRunTree = %+v", got.Run)
	}
	if got.Steps[0].Path != "main" || got.Steps[0].Kind != store.KindGroup || len(got.Steps[0].Executions) != 0 {
		t.Fatalf("group step = %+v", got.Steps[0])
	}
	st := got.Steps[1]
	if st.Path != "main/a" || st.Kind != store.KindStep || st.Status != store.StepPending || len(st.Executions) != 1 || st.Executions[0].ID != tree.Steps[1].Executions[0].ID {
		t.Fatalf("executable step = %+v", st)
	}
	assertJSONEqual(t, st.Input, `{"cmd":"true"}`)
	assertJSONEqual(t, got.Options, `{"env":"prod"}`)
	assertJSONEqual(t, got.Trigger, `{"type":"manual"}`)
	if run, err := s.GetRun(ctx, tree.ID); err != nil || run.Version != 1 {
		t.Fatalf("GetRun = %+v, %v", run, err)
	}
	if step, err := s.GetStepInstance(ctx, st.ID); err != nil || step.Path != "main/a" {
		t.Fatalf("GetStepInstance = %+v, %v", step, err)
	}

	for _, id := range []string{"nope"} {
		if _, err := s.GetRun(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetRun missing: %v", err)
		}
		if _, err := s.GetRunTree(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetRunTree missing: %v", err)
		}
		if _, err := s.GetStepInstance(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetStepInstance missing: %v", err)
		}
	}
	_ = job
}

func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("got %q is not json: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	ga, _ := json.Marshal(a)
	gb, _ := json.Marshal(b)
	if string(ga) != string(gb) {
		t.Fatalf("json = %s; want %s", ga, gb)
	}
}

func testCreateRunErrors(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	job, ver := mustJob(t, s, "j")
	base := store.NewRun{JobID: job.ID, JobVersionID: ver.ID}
	cases := []struct {
		name string
		mod  func(nr *store.NewRun)
		want error
	}{
		{"unknown job", func(nr *store.NewRun) { nr.JobID = "nope" }, store.ErrNotFound},
		{"unknown version", func(nr *store.NewRun) { nr.JobVersionID = "nope" }, store.ErrNotFound},
		{"missing job", func(nr *store.NewRun) { nr.JobID = "" }, store.ErrInvalid},
		{"bad options", func(nr *store.NewRun) { nr.Options = json.RawMessage(`{`) }, store.ErrInvalid},
		{"empty path", func(nr *store.NewRun) { nr.Steps = []store.NewStepInstance{{}} }, store.ErrInvalid},
		{"no pool", func(nr *store.NewRun) {
			nr.Steps = []store.NewStepInstance{{Path: "a", Execution: &store.NewExecution{}}}
		}, store.ErrInvalid},
		{"duplicate path", func(nr *store.NewRun) {
			nr.Steps = []store.NewStepInstance{{Path: "a"}, {Path: "a"}}
		}, store.ErrConflict},
	}
	for _, tc := range cases {
		nr := base
		tc.mod(&nr)
		if _, err := s.CreateRun(ctx, nr, t0); !errors.Is(err, tc.want) {
			t.Errorf("CreateRun %s: %v; want %v", tc.name, err, tc.want)
		}
	}
	// A rejected create leaves no run behind.
	nr := base
	nr.Steps = []store.NewStepInstance{{Path: "a", Execution: execSpec("p")}, {Path: "b", Execution: &store.NewExecution{}}}
	if _, err := s.CreateRun(ctx, nr, t0); err == nil {
		t.Fatal("CreateRun with bad second step succeeded")
	}
	if page, err := s.ListRuns(ctx, job.ID, "", 10); err != nil || len(page.Runs) != 0 {
		t.Fatalf("runs after failed create = %+v, %v; want none", page.Runs, err)
	}
}

func testRunStatusCAS(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	_, tree := mustRun(t, s, "j")
	t1, t2 := t0.Add(time.Second), t0.Add(2*time.Second)

	run, err := s.UpdateRunStatus(ctx, tree.ID, 1, store.RunRunning, t1)
	if err != nil || run.Version != 2 || run.StartedAt == nil || !run.StartedAt.Equal(t1) || run.FinishedAt != nil {
		t.Fatalf("to running = %+v, %v", run, err)
	}
	if _, err := s.UpdateRunStatus(ctx, tree.ID, 1, store.RunFailed, t2); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale version: %v; want ErrStale", err)
	}
	if got, _ := s.GetRun(ctx, tree.ID); got.Status != store.RunRunning || got.Version != 2 {
		t.Fatalf("stale write mutated run: %+v", got)
	}
	run, err = s.UpdateRunStatus(ctx, tree.ID, 2, store.RunSucceeded, t2)
	if err != nil || run.Version != 3 || !run.StartedAt.Equal(t1) || run.FinishedAt == nil || !run.FinishedAt.Equal(t2) {
		t.Fatalf("to succeeded = %+v, %v", run, err)
	}
	if got, _ := s.GetRun(ctx, tree.ID); got.Status != run.Status || got.Version != run.Version {
		t.Fatalf("GetRun = %+v; want %+v", got, run)
	}
	if _, err := s.UpdateRunStatus(ctx, "nope", 1, store.RunRunning, t1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing run: %v; want ErrNotFound", err)
	}

	_, tree2 := mustRun(t, s, "j2")
	res := raceN(8, func(int) error {
		_, err := s.UpdateRunStatus(ctx, tree2.ID, 1, store.RunRunning, t1)
		return err
	})
	if res.won != 1 || res.stale != 7 || len(res.other) != 0 {
		t.Fatalf("UpdateRunStatus winners=%d stale=%d other=%v; want 1 and 7", res.won, res.stale, res.other)
	}
}

func testAdvancePrimitives(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	_, tree := mustRun(t, s, "j")
	step := tree.Steps[0]

	nodes, err := s.AddStepInstances(ctx, tree.ID, 1, []store.NewStepInstance{
		{Path: "main/b", Plugin: "shell", Execution: execSpec("default")},
		{Path: "main/c"},
	}, t0)
	if err != nil || len(nodes) != 2 || len(nodes[0].Executions) != 1 || len(nodes[1].Executions) != 0 {
		t.Fatalf("AddStepInstances = %+v, %v", nodes, err)
	}
	if run, _ := s.GetRun(ctx, tree.ID); run.Version != 2 {
		t.Fatalf("version after add = %d; want 2", run.Version)
	}
	if _, err := s.AddStepInstances(ctx, tree.ID, 1, []store.NewStepInstance{{Path: "main/d"}}, t0); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale add: %v; want ErrStale", err)
	}
	if _, err := s.AddStepInstances(ctx, tree.ID, 2, []store.NewStepInstance{{Path: "main/b"}}, t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate path: %v; want ErrConflict", err)
	}
	if run, _ := s.GetRun(ctx, tree.ID); run.Version != 2 {
		t.Fatalf("failed add must roll back the version bump; version = %d", run.Version)
	}
	if _, err := s.AddStepInstances(ctx, "nope", 1, []store.NewStepInstance{{Path: "x"}}, t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("add to missing run: %v; want ErrNotFound", err)
	}

	// Retry: the next attempt gets attempt_no 2 and a distinct attempt_id.
	ex, err := s.ScheduleExecution(ctx, tree.ID, 2, step.ID, store.NewExecution{Pool: "default", ScheduledAt: t0.Add(time.Hour), Priority: 3}, t0)
	if err != nil || ex.AttemptNo != 2 || ex.AttemptID == step.Executions[0].AttemptID || ex.Priority != 3 {
		t.Fatalf("ScheduleExecution = %+v, %v", ex, err)
	}
	if _, err := s.ScheduleExecution(ctx, tree.ID, 2, step.ID, store.NewExecution{Pool: "default"}, t0); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale schedule: %v; want ErrStale", err)
	}
	if _, err := s.ScheduleExecution(ctx, tree.ID, 3, "nope", store.NewExecution{Pool: "default"}, t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("schedule unknown step: %v; want ErrNotFound", err)
	}
	_, other := mustRun(t, s, "j2")
	if _, err := s.ScheduleExecution(ctx, tree.ID, 3, other.Steps[0].ID, store.NewExecution{Pool: "default"}, t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("schedule step of another run: %v; want ErrNotFound", err)
	}
	got, _ := s.GetRunTree(ctx, tree.ID)
	if got.Version != 3 || len(got.Steps) != 3 || len(got.Steps[0].Executions) != 2 || got.Steps[0].Executions[1].AttemptNo != 2 {
		t.Fatalf("tree after advance: version=%d steps=%d", got.Version, len(got.Steps))
	}
}

func testStepInstanceCAS(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	_, tree := mustRun(t, s, "j")
	id := tree.Steps[0].ID
	t1, t2 := t0.Add(time.Second), t0.Add(2*time.Second)

	st, err := s.UpdateStepInstance(ctx, store.StepUpdate{ID: id, ExpectedStatus: store.StepPending, Status: store.StepRunning}, t1)
	if err != nil || st.Status != store.StepRunning || st.StartedAt == nil || !st.StartedAt.Equal(t1) {
		t.Fatalf("to running = %+v, %v", st, err)
	}
	if _, err := s.UpdateStepInstance(ctx, store.StepUpdate{ID: id, ExpectedStatus: store.StepPending, Status: store.StepFailed}, t2); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale status: %v; want ErrStale", err)
	}
	st, err = s.UpdateStepInstance(ctx, store.StepUpdate{
		ID: id, ExpectedStatus: store.StepRunning, Status: store.StepSucceeded, Outputs: json.RawMessage(`{"rc":0}`),
	}, t2)
	if err != nil || st.FinishedAt == nil || !st.FinishedAt.Equal(t2) {
		t.Fatalf("to succeeded = %+v, %v", st, err)
	}
	got, _ := s.GetStepInstance(ctx, id)
	assertJSONEqual(t, got.Outputs, `{"rc":0}`)
	if got.Status != store.StepSucceeded || !got.StartedAt.Equal(t1) {
		t.Fatalf("stored step = %+v", got)
	}
	if _, err := s.UpdateStepInstance(ctx, store.StepUpdate{ID: "nope", ExpectedStatus: store.StepPending, Status: store.StepRunning}, t1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing step: %v; want ErrNotFound", err)
	}
}

func testRequestCancel(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	_, tree := mustRun(t, s, "j")
	tc := t0.Add(time.Minute)

	run, err := s.RequestCancel(ctx, tree.ID, tc)
	if err != nil || run.CancelRequestedAt == nil || !run.CancelRequestedAt.Equal(tc) || run.Version != 2 {
		t.Fatalf("RequestCancel = %+v, %v", run, err)
	}
	if _, err := s.RequestCancel(ctx, tree.ID, tc.Add(time.Second)); !errors.Is(err, store.ErrStale) {
		t.Fatalf("second cancel: %v; want ErrStale", err)
	}
	if got, _ := s.GetRun(ctx, tree.ID); !got.CancelRequestedAt.Equal(tc) {
		t.Fatalf("second cancel overwrote the first: %+v", got)
	}
	if _, err := s.RequestCancel(ctx, "nope", tc); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing run: %v; want ErrNotFound", err)
	}
	// A terminal run cannot be canceled.
	_, done := mustRun(t, s, "j2")
	if _, err := s.UpdateRunStatus(ctx, done.ID, 1, store.RunSucceeded, tc); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestCancel(ctx, done.ID, tc); !errors.Is(err, store.ErrStale) {
		t.Fatalf("cancel terminal run: %v; want ErrStale", err)
	}
}

func testListRuns(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	job, ver := mustJob(t, s, "j")
	var ids []string
	for range 5 {
		tree, err := s.CreateRun(ctx, store.NewRun{JobID: job.ID, JobVersionID: ver.ID}, t0)
		if err != nil {
			t.Fatal(err)
		}
		ids = append([]string{tree.ID}, ids...) // newest first
	}
	var got []string
	cursor := ""
	for range 10 {
		page, err := s.ListRuns(ctx, job.ID, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Runs {
			got = append(got, r.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(got) != len(ids) {
		t.Fatalf("ListRuns = %v; want %v", got, ids)
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Fatalf("ListRuns order = %v; want %v (newest first)", got, ids)
		}
	}
}
