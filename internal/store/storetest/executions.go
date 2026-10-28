package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

const leaseTTL = time.Minute

var claimNow = t0.Add(time.Hour)

func claim(t *testing.T, s store.Store, pool string, limit int) []store.Execution {
	t.Helper()
	got, err := s.ClaimExecutions(context.Background(), store.ClaimParams{
		AgentID: "agent-1", ControllerID: "ctrl-1", Pool: pool, Limit: limit, LeaseTTL: leaseTTL,
	}, claimNow)
	if err != nil {
		t.Fatalf("ClaimExecutions: %v", err)
	}
	return got
}

// claimedOne creates a run with one queued execution and claims it.
func claimedOne(t *testing.T, s store.Store) store.Execution {
	t.Helper()
	mustRun(t, s, "claimed")
	got := claim(t, s, "default", 1)
	if len(got) != 1 {
		t.Fatalf("claimed %d executions; want 1", len(got))
	}
	return got[0]
}

func mustState(t *testing.T, s store.Store, id string, want store.ExecutionState) store.Execution {
	t.Helper()
	ex, err := s.GetExecution(context.Background(), id)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if ex.State != want {
		t.Fatalf("execution state = %s; want %s", ex.State, want)
	}
	return ex
}

func testClaim(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	step := func(path, pool string, prio int, at time.Time) store.NewStepInstance {
		return store.NewStepInstance{Path: path, Plugin: "shell", Execution: &store.NewExecution{Pool: pool, Priority: prio, ScheduledAt: at}}
	}
	_, tree := mustRun(t, s, "j",
		step("a", "default", 5, t0), step("b", "default", 1, t0), step("c", "other", 0, t0),
		step("d", "default", 0, claimNow.Add(time.Hour)))
	orig := map[string]string{}
	for _, st := range tree.Steps {
		orig[st.Path] = st.Executions[0].AttemptID
	}

	first := claim(t, s, "default", 1)
	if len(first) != 1 || first[0].StepInstanceID != tree.Steps[1].ID {
		t.Fatalf("first claim = %+v; want step b (best priority)", first)
	}
	e := first[0]
	if e.State != store.ExecDispatched || e.AgentID != "agent-1" || e.ControllerID != "ctrl-1" ||
		e.LeaseExpiresAt == nil || !e.LeaseExpiresAt.Equal(claimNow.Add(leaseTTL)) {
		t.Fatalf("claimed execution = %+v", e)
	}
	if e.AttemptID == orig["b"] {
		t.Fatal("claim must rotate attempt_id")
	}
	if _, err := s.GetExecutionByAttempt(ctx, orig["b"]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old attempt still resolves: %v", err)
	}
	if got, err := s.GetExecutionByAttempt(ctx, e.AttemptID); err != nil || got.ID != e.ID {
		t.Fatalf("GetExecutionByAttempt = %+v, %v", got, err)
	}

	rest := claim(t, s, "default", 10)
	if len(rest) != 1 || rest[0].StepInstanceID != tree.Steps[0].ID {
		t.Fatalf("second claim = %+v; want only step a (c is another pool, d not due)", rest)
	}
	if again := claim(t, s, "default", 10); len(again) != 0 {
		t.Fatalf("third claim = %+v; want none", again)
	}
	if other := claim(t, s, "other", 10); len(other) != 1 {
		t.Fatalf("other pool claim = %+v; want 1", other)
	}

	for _, p := range []store.ClaimParams{
		{ControllerID: "c", Pool: "p", Limit: 1, LeaseTTL: time.Second},
		{AgentID: "a", ControllerID: "c", Pool: "p", Limit: 0, LeaseTTL: time.Second},
		{AgentID: "a", ControllerID: "c", Pool: "p", Limit: 1},
	} {
		if _, err := s.ClaimExecutions(ctx, p, claimNow); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("ClaimExecutions(%+v): %v; want ErrInvalid", p, err)
		}
	}
}

// testClaimConcurrent proves the dispatch claim hands each execution to
// exactly one claimer (FOR UPDATE SKIP LOCKED on Postgres).
func testClaimConcurrent(t *testing.T, s store.Store) {
	migrated(t, s)
	mustRun(t, s, "one")
	var wins int
	var mu sync.Mutex
	res := raceN(8, func(i int) error {
		got, err := s.ClaimExecutions(context.Background(), store.ClaimParams{
			AgentID: fmt.Sprint("agent-", i), ControllerID: "ctrl", Pool: "default", Limit: 1, LeaseTTL: leaseTTL,
		}, claimNow)
		mu.Lock()
		wins += len(got)
		mu.Unlock()
		return err
	})
	if len(res.other) != 0 || wins != 1 {
		t.Fatalf("single execution: claimed %d times, errors %v; want exactly 1", wins, res.other)
	}

	// Many executions, many claimers: every execution is claimed exactly once.
	steps := make([]store.NewStepInstance, 20)
	for i := range steps {
		steps[i] = store.NewStepInstance{Path: fmt.Sprint("s", i), Execution: &store.NewExecution{Pool: "bulk", ScheduledAt: t0}}
	}
	mustRun(t, s, "many", steps...)
	seen := map[string]int{}
	res = raceN(8, func(i int) error {
		got, err := s.ClaimExecutions(context.Background(), store.ClaimParams{
			AgentID: fmt.Sprint("agent-", i), ControllerID: "ctrl", Pool: "bulk", Limit: 5, LeaseTTL: leaseTTL,
		}, claimNow)
		mu.Lock()
		defer mu.Unlock()
		for _, e := range got {
			seen[e.ID]++
		}
		return err
	})
	if len(res.other) != 0 || len(seen) != 20 {
		t.Fatalf("claimed %d distinct executions, errors %v; want 20", len(seen), res.other)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("execution %s claimed %d times", id, n)
		}
	}
}

func testExecutionLifecycle(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	e := claimedOne(t, s)
	t1, t2 := claimNow.Add(time.Second), claimNow.Add(2*time.Second)

	if err := s.MarkExecutionRunning(ctx, e.ID, "wrong-attempt", t1); !errors.Is(err, store.ErrStale) {
		t.Fatalf("MarkRunning stale attempt: %v; want ErrStale", err)
	}
	mustState(t, s, e.ID, store.ExecDispatched)
	if err := s.MarkExecutionRunning(ctx, "nope", e.AttemptID, t1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("MarkRunning missing: %v; want ErrNotFound", err)
	}
	if err := s.MarkExecutionRunning(ctx, e.ID, e.AttemptID, t1); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if got := mustState(t, s, e.ID, store.ExecRunning); got.StartedAt == nil || !got.StartedAt.Equal(t1) {
		t.Fatalf("started_at = %v; want %v", got.StartedAt, t1)
	}
	if err := s.MarkExecutionRunning(ctx, e.ID, e.AttemptID, t1); !errors.Is(err, store.ErrStale) {
		t.Fatalf("MarkRunning twice: %v; want ErrStale", err)
	}

	for _, st := range []store.ExecutionState{store.ExecQueued, store.ExecRunning, store.ExecLost, ""} {
		if _, err := s.CompleteExecution(ctx, store.Completion{ExecutionID: e.ID, AttemptID: e.AttemptID, State: st}, t2); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("Complete into %q: %v; want ErrInvalid", st, err)
		}
	}
	if _, err := s.CompleteExecution(ctx, store.Completion{ExecutionID: "nope", AttemptID: e.AttemptID, State: store.ExecSucceeded}, t2); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Complete missing: %v; want ErrNotFound", err)
	}
	done, err := s.CompleteExecution(ctx, store.Completion{
		ExecutionID: e.ID, AttemptID: e.AttemptID, State: store.ExecFailed,
		Outputs: json.RawMessage(`{"rc":2}`), Error: "exit status 2",
	}, t2)
	if err != nil || done.State != store.ExecFailed || done.Error != "exit status 2" || done.FinishedAt == nil || !done.FinishedAt.Equal(t2) {
		t.Fatalf("Complete = %+v, %v", done, err)
	}
	assertJSONEqual(t, done.Outputs, `{"rc":2}`)
	if _, err := s.CompleteExecution(ctx, store.Completion{ExecutionID: e.ID, AttemptID: e.AttemptID, State: store.ExecSucceeded}, t2); !errors.Is(err, store.ErrStale) {
		t.Fatalf("Complete twice: %v; want ErrStale", err)
	}
	if got := mustState(t, s, e.ID, store.ExecFailed); got.Error != "exit status 2" {
		t.Fatalf("second completion mutated the execution: %+v", got)
	}
}

// testFencing proves a stale attempt_id never mutates state: every
// agent-reported transition is rejected once the attempt has been rotated.
func testFencing(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	first := claimedOne(t, s)
	if err := s.RequeueExecution(ctx, first.ID, first.AttemptID, claimNow); err != nil {
		t.Fatal(err)
	}
	second := claim(t, s, "default", 1)
	if len(second) != 1 || second[0].ID != first.ID || second[0].AttemptID == first.AttemptID {
		t.Fatalf("reclaim = %+v; want same execution with a new attempt_id", second)
	}
	cur := second[0]
	before := mustState(t, s, cur.ID, store.ExecDispatched)

	stale := first.AttemptID
	checks := map[string]error{
		"MarkExecutionRunning": s.MarkExecutionRunning(ctx, cur.ID, stale, claimNow),
		"RequeueExecution":     s.RequeueExecution(ctx, cur.ID, stale, claimNow),
	}
	_, checks["CompleteExecution"] = s.CompleteExecution(ctx, store.Completion{ExecutionID: cur.ID, AttemptID: stale, State: store.ExecSucceeded}, claimNow)
	_, checks["InsertLogChunk"] = s.InsertLogChunk(ctx, stale, store.NewLogChunk{
		ExecutionID: cur.ID, SeqFrom: 1, SeqTo: 2, Backend: "db", ObjectKey: "k", TsFirst: t0, TsLast: t0,
	}, claimNow)
	for name, err := range checks {
		if !errors.Is(err, store.ErrStale) {
			t.Errorf("%s with stale attempt: %v; want ErrStale", name, err)
		}
	}
	after := mustState(t, s, cur.ID, store.ExecDispatched)
	if after.AttemptID != before.AttemptID || after.AgentID != before.AgentID || after.FinishedAt != nil || after.Outputs == nil {
		t.Fatalf("stale reports mutated the execution:\nbefore %+v\nafter  %+v", before, after)
	}
	if chunks, _ := s.ListLogChunks(ctx, cur.ID, 0, 0, 10); len(chunks) != 0 {
		t.Fatalf("stale log chunk was stored: %+v", chunks)
	}
	if _, err := s.CompleteExecution(ctx, store.Completion{ExecutionID: cur.ID, AttemptID: cur.AttemptID, State: store.ExecSucceeded}, claimNow); err != nil {
		t.Fatalf("current attempt must still complete: %v", err)
	}
}

func testCompleteConcurrent(t *testing.T, s store.Store) {
	migrated(t, s)
	e := claimedOne(t, s)
	res := raceN(8, func(int) error {
		_, err := s.CompleteExecution(context.Background(), store.Completion{ExecutionID: e.ID, AttemptID: e.AttemptID, State: store.ExecSucceeded}, claimNow)
		return err
	})
	if res.won != 1 || res.stale != 7 || len(res.other) != 0 {
		t.Fatalf("Complete winners=%d stale=%d other=%v; want 1 and 7", res.won, res.stale, res.other)
	}
}

func testRequeue(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	e := claimedOne(t, s)
	later := claimNow.Add(time.Hour)

	if err := s.RequeueExecution(ctx, e.ID, "wrong", later); !errors.Is(err, store.ErrStale) {
		t.Fatalf("Requeue wrong attempt: %v; want ErrStale", err)
	}
	if err := s.RequeueExecution(ctx, "nope", e.AttemptID, later); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Requeue missing: %v; want ErrNotFound", err)
	}
	if err := s.RequeueExecution(ctx, e.ID, e.AttemptID, later); err != nil {
		t.Fatal(err)
	}
	q := mustState(t, s, e.ID, store.ExecQueued)
	if q.AgentID != "" || q.ControllerID != "" || q.LeaseExpiresAt != nil || !q.ScheduledAt.Equal(later) {
		t.Fatalf("requeued execution = %+v; ownership must be cleared", q)
	}
	if got := claim(t, s, "default", 1); len(got) != 0 {
		t.Fatalf("requeued execution claimed before it is due: %+v", got)
	}

	// A running execution cannot be requeued (the agent already accepted it).
	e2 := func() store.Execution {
		mustRun(t, s, "second")
		return claim(t, s, "default", 1)[0]
	}()
	if err := s.MarkExecutionRunning(ctx, e2.ID, e2.AttemptID, claimNow); err != nil {
		t.Fatal(err)
	}
	if err := s.RequeueExecution(ctx, e2.ID, e2.AttemptID, later); !errors.Is(err, store.ErrStale) {
		t.Fatalf("Requeue running: %v; want ErrStale", err)
	}
}

func testLeases(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	steps := []store.NewStepInstance{
		{Path: "a", Execution: execSpec("default")}, {Path: "b", Execution: execSpec("default")},
	}
	mustRun(t, s, "j", steps...)
	got := claim(t, s, "default", 2)
	if len(got) != 2 {
		t.Fatalf("claimed %d; want 2", len(got))
	}
	a, b := got[0], got[1]
	if err := s.MarkExecutionRunning(ctx, a.ID, a.AttemptID, claimNow); err != nil {
		t.Fatal(err)
	}

	if n, err := s.RenewLeases(ctx, "other-ctrl", []string{a.AttemptID, b.AttemptID}, claimNow.Add(time.Hour)); err != nil || len(n) != 0 {
		t.Fatalf("renew by non-owner = %v, %v; want none", n, err)
	}
	if n, err := s.RenewLeases(ctx, "ctrl-1", nil, claimNow); err != nil || len(n) != 0 {
		t.Fatalf("renew empty = %v, %v", n, err)
	}
	extended := claimNow.Add(10 * time.Minute)
	renewed, err := s.RenewLeases(ctx, "ctrl-1", []string{a.AttemptID, "unknown-attempt"}, extended)
	if err != nil || len(renewed) != 1 || renewed[0] != a.AttemptID {
		t.Fatalf("RenewLeases = %v, %v; want only a", renewed, err)
	}
	if got := mustState(t, s, a.ID, store.ExecRunning); !got.LeaseExpiresAt.Equal(extended) {
		t.Fatalf("lease = %v; want %v", got.LeaseExpiresAt, extended)
	}

	if lost, err := s.ExpireLeases(ctx, claimNow.Add(leaseTTL), 10); err != nil || len(lost) != 0 {
		t.Fatalf("ExpireLeases at exact lease end = %+v, %v; want none", lost, err)
	}
	tExpire := claimNow.Add(leaseTTL + time.Second)
	lost, err := s.ExpireLeases(ctx, tExpire, 10)
	if err != nil || len(lost) != 1 || lost[0].ID != b.ID || lost[0].State != store.ExecLost || lost[0].FinishedAt == nil || !lost[0].FinishedAt.Equal(tExpire) {
		t.Fatalf("ExpireLeases = %+v, %v; want only b lost", lost, err)
	}
	mustState(t, s, a.ID, store.ExecRunning)
	if again, _ := s.ExpireLeases(ctx, tExpire, 10); len(again) != 0 {
		t.Fatalf("ExpireLeases twice = %+v; a lost execution must not be reported again", again)
	}

	// A lost attempt is fenced out: it can neither renew nor complete.
	if n, _ := s.RenewLeases(ctx, "ctrl-1", []string{b.AttemptID}, extended); len(n) != 0 {
		t.Fatalf("lost attempt renewed: %v", n)
	}
	if _, err := s.CompleteExecution(ctx, store.Completion{ExecutionID: b.ID, AttemptID: b.AttemptID, State: store.ExecSucceeded}, tExpire); !errors.Is(err, store.ErrStale) {
		t.Fatalf("Complete lost attempt: %v; want ErrStale", err)
	}
	mustState(t, s, b.ID, store.ExecLost)

	lost, err = s.ExpireLeases(ctx, extended.Add(time.Second), 10)
	if err != nil || len(lost) != 1 || lost[0].ID != a.ID {
		t.Fatalf("ExpireLeases after extension = %+v, %v; want a", lost, err)
	}
}
