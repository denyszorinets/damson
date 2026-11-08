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

func spec(n string) json.RawMessage { return json.RawMessage(`{"name":"` + n + `"}`) }

func jobOps(path, name, sum string) store.ApplyOp {
	return store.ApplyOp{Kind: store.OpJobCreate, Source: "files:main", ProjectPath: path, Name: name, Spec: spec(name + sum), Checksum: sum}
}

func stateJob(t *testing.T, s store.Store, source string, ids []string, name string) store.ApplyJob {
	t.Helper()
	st, err := s.ApplyState(context.Background(), source, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range st.Jobs {
		if j.Name == name {
			return j
		}
	}
	t.Fatalf("job %q not in the apply state %+v", name, st.Jobs)
	return store.ApplyJob{}
}

func testApplyPlan(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	rules := []store.AccessRule{{Role: store.RoleRunner, Rule: "true"}}
	ops := []store.ApplyOp{
		{Kind: store.OpProjectCreate, Source: "files:main", Path: "infra", Description: "Infra", AccessRules: rules, NodeScope: "node.name != ''"},
		{Kind: store.OpProjectCreate, Source: "files:main", Path: "infra/billing"},
		jobOps("infra/billing", "deploy", "c1"),
	}
	if err := s.ApplyPlan(context.Background(), ops, t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("apply without an actor = %v; want ErrInvalid", err)
	}
	if err := s.ApplyPlan(ctx, ops, t0); err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	p, err := s.GetProjectByPath(ctx, "infra")
	if err != nil || p.ManagedBy != "files:main" || p.Description != "Infra" || len(p.AccessRules) != 1 || p.NodeScope == "" {
		t.Fatalf("project = %+v, %v", p, err)
	}
	child, _ := s.GetProjectByPath(ctx, "infra/billing")
	if child.ParentID != p.ID {
		t.Fatalf("child parent = %q, want %q", child.ParentID, p.ID)
	}
	j := stateJob(t, s, "files:main", nil, "deploy")
	if j.ManagedBy != "files:main" || j.Checksum != "c1" || j.CurrentVersion != 1 || j.ProjectPath != "infra/billing" || j.ArchivedAt != nil {
		t.Fatalf("job = %+v", j)
	}
	evs, err := s.ListAuditEvents(ctx, store.AuditFilter{Project: "infra"}, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		seen[e.Action] = true
	}
	for _, want := range []string{store.ActionProjectCreate, store.ActionProjectAccessRulesUpdate, store.ActionProjectNodeScopeUpdate, store.ActionJobCreate} {
		if !seen[want] {
			t.Errorf("no %s event; got %v", want, seen)
		}
	}

	// A new version, as the source that owns the job.
	up := store.ApplyOp{Kind: store.OpJobUpdate, Source: "files:main", ExpectManaged: "files:main", ID: j.ID, ExpectCurrent: 1, Spec: spec("v2"), Checksum: "c2"}
	if err := s.ApplyPlan(ctx, []store.ApplyOp{up}, t0); err != nil {
		t.Fatal(err)
	}
	if j2 := stateJob(t, s, "files:main", nil, "deploy"); j2.CurrentVersion != 2 || j2.Checksum != "c2" {
		t.Fatalf("after update = %+v", j2)
	}
}

func testApplyAllOrNothing(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	if err := s.ApplyPlan(ctx, []store.ApplyOp{jobOps("default", "a", "c1")}, t0); err != nil {
		t.Fatal(err)
	}
	a := stateJob(t, s, "files:main", nil, "a")
	ops := []store.ApplyOp{
		jobOps("default", "b", "c1"),
		{Kind: store.OpJobUpdate, Source: "files:main", ExpectManaged: "files:main", ID: a.ID, ExpectCurrent: 7, Spec: spec("x"), Checksum: "x"}, // wrong version
	}
	if err := s.ApplyPlan(ctx, ops, t0); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale op = %v; want ErrStale", err)
	}
	st, _ := s.ApplyState(ctx, "files:main", nil)
	for _, j := range st.Jobs {
		if j.Name == "b" {
			t.Fatal("an earlier op survived a failed plan")
		}
	}
	// A taken name is a conflict, also rolling back.
	if err := s.ApplyPlan(ctx, []store.ApplyOp{jobOps("default", "c", "c1"), jobOps("default", "a", "c9")}, t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name = %v; want ErrConflict", err)
	}
	if st, _ := s.ApplyState(ctx, "files:main", nil); len(st.Jobs) != 1 {
		t.Fatalf("jobs after a failed plan = %d, want 1", len(st.Jobs))
	}
}

// Two applies planned from the same state: exactly one commits.
func testApplyRace(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	if err := s.ApplyPlan(ctx, []store.ApplyOp{jobOps("default", "a", "c1")}, t0); err != nil {
		t.Fatal(err)
	}
	a := stateJob(t, s, "files:main", nil, "a")
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.ApplyPlan(ctx, []store.ApplyOp{{
				Kind: store.OpJobUpdate, Source: "files:main", ExpectManaged: "files:main", ID: a.ID, ExpectCurrent: 1,
				Spec: spec("v"), Checksum: "c" + string(rune('2'+i)),
			}}, t0)
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, store.ErrStale):
			t.Fatalf("loser = %v; want ErrStale", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d applies committed, want 1", wins)
	}
	if got := stateJob(t, s, "files:main", nil, "a"); got.CurrentVersion != 2 {
		t.Fatalf("version = %d, want 2", got.CurrentVersion)
	}
}

func testManagedGuards(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	if err := s.ApplyPlan(ctx, []store.ApplyOp{
		{Kind: store.OpProjectCreate, Source: "files:main", Path: "infra"}, jobOps("infra", "deploy", "c1"),
	}, t0); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetProjectByPath(ctx, "infra")
	j := stateJob(t, s, "files:main", nil, "deploy")
	var me *store.ManagedError
	if _, err := s.CreateJobVersion(ctx, j.ID, spec("ui"), "cx", 1, t0); !errors.As(err, &me) || me.By != "files:main" {
		t.Fatalf("CreateJobVersion on a managed job = %v; want ManagedError", err)
	}
	if _, err := s.UpdateProject(ctx, p.ID, "x", p.Version, t0); !errors.As(err, &me) {
		t.Fatalf("UpdateProject = %v; want ManagedError", err)
	}
	if _, err := s.SetProjectAccessRules(ctx, p.ID, nil, p.Version, t0); !errors.As(err, &me) {
		t.Fatalf("SetProjectAccessRules = %v; want ManagedError", err)
	}
	if _, err := s.SetProjectNodeScope(ctx, p.ID, "", p.Version, t0); !errors.As(err, &me) {
		t.Fatalf("SetProjectNodeScope = %v; want ManagedError", err)
	}
	if err := s.DeleteProject(ctx, p.ID, t0); !errors.As(err, &me) {
		t.Fatalf("DeleteProject = %v; want ManagedError", err)
	}
	if !errors.Is(me, store.ErrConflict) {
		t.Fatal("a ManagedError must match ErrConflict")
	}
	// Releasing the project to the UI makes it editable again.
	rel := store.ApplyOp{Kind: store.OpProjectUpdate, Source: store.SourceUI, ExpectManaged: "files:main", ID: p.ID, ExpectVersion: p.Version}
	if err := s.ApplyPlan(ctx, []store.ApplyOp{rel}, t0); err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetProjectByPath(ctx, "infra")
	if p.ManagedBy != store.SourceUI {
		t.Fatalf("managed_by = %q after release", p.ManagedBy)
	}
	if _, err := s.UpdateProject(ctx, p.ID, "now editable", p.Version, t0); err != nil {
		t.Fatalf("UpdateProject after release: %v", err)
	}
	evs, _ := s.ListAuditEvents(ctx, store.AuditFilter{Project: "infra"}, 50)
	released := false
	for _, e := range evs {
		released = released || e.Action == store.ActionProjectManage
	}
	if !released {
		t.Fatal("no project.manage event for the release")
	}
}

func testArchiveRestore(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	if err := s.ApplyPlan(ctx, []store.ApplyOp{jobOps("default", "gone", "c1")}, t0); err != nil {
		t.Fatal(err)
	}
	j := stateJob(t, s, "files:main", nil, "gone")
	arch := store.ApplyOp{Kind: store.OpJobArchive, Source: "files:main", ExpectManaged: "files:main", ID: j.ID, ExpectCurrent: 1}
	if err := s.ApplyPlan(ctx, []store.ApplyOp{arch}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPlan(ctx, []store.ApplyOp{arch}, t0); !errors.Is(err, store.ErrStale) {
		t.Fatalf("archiving twice = %v; want ErrStale", err)
	}
	page, err := s.ListJobs(ctx, store.DefaultProjectID, "", 100)
	if err != nil || len(page.Jobs) != 0 {
		t.Fatalf("an archived job is listed: %+v %v", page.Jobs, err)
	}
	got, err := s.GetJob(ctx, j.ID)
	if err != nil || got.ArchivedAt == nil {
		t.Fatalf("GetJob = %+v %v; want archived", got, err)
	}
	var me *store.ManagedError
	if _, err := s.CreateJobVersion(ctx, j.ID, spec("x"), "x", 1, t0); !errors.As(err, &me) {
		t.Fatalf("version of an archived job = %v; want ManagedError", err)
	}
	restore := store.ApplyOp{Kind: store.OpJobUpdate, Source: "files:main", ExpectManaged: "files:main", ID: j.ID, ExpectCurrent: 1, Restore: true, Spec: spec("back"), Checksum: "c2"}
	if err := s.ApplyPlan(ctx, []store.ApplyOp{restore}, t0); err != nil {
		t.Fatal(err)
	}
	back := stateJob(t, s, "files:main", nil, "gone")
	if back.ArchivedAt != nil || back.CurrentVersion != 2 || back.Checksum != "c2" {
		t.Fatalf("restored = %+v", back)
	}
	if page, _ := s.ListJobs(ctx, store.DefaultProjectID, "", 100); len(page.Jobs) != 1 {
		t.Fatalf("restored job not listed: %+v", page.Jobs)
	}
}

func testAdoptJob(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	j, _, err := s.CreateJob(ctx, store.NewJob{ProjectID: store.DefaultProjectID, Name: "legacy", Spec: spec("legacy"), Checksum: "c1"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	adopt := store.ApplyOp{Kind: store.OpJobUpdate, Source: "files:main", ExpectManaged: store.SourceUI, ID: j.ID, ExpectCurrent: 1}
	if err := s.ApplyPlan(ctx, []store.ApplyOp{adopt}, t0); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetJob(ctx, j.ID)
	if got.ManagedBy != "files:main" || got.CurrentVersion != 1 {
		t.Fatalf("adopted = %+v", got)
	}
	// The expectation is a CAS: adopting again from "ui" loses.
	if err := s.ApplyPlan(ctx, []store.ApplyOp{adopt}, t0); !errors.Is(err, store.ErrStale) {
		t.Fatalf("second adopt = %v; want ErrStale", err)
	}
}

// A schedule created after the plan was made keeps the job from being archived:
// the compare-and-swap loses and the whole apply rolls back.
func testArchiveBlockedByLateSchedule(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	if err := s.ApplyPlan(ctx, []store.ApplyOp{jobOps("default", "busy", "c1")}, t0); err != nil {
		t.Fatal(err)
	}
	j := stateJob(t, s, "files:main", nil, "busy")
	if j.Bindings != 0 {
		t.Fatalf("bindings = %d before the schedule", j.Bindings)
	}
	if _, err := s.CreateSchedule(ctx, newSchedule("late", j.ID, "nightly", t0.Add(time.Hour)), t0); err != nil {
		t.Fatal(err)
	}
	arch := store.ApplyOp{Kind: store.OpJobArchive, Source: "files:main", ExpectManaged: "files:main", ID: j.ID, ExpectCurrent: 1}
	if err := s.ApplyPlan(ctx, []store.ApplyOp{arch}, t0); !errors.Is(err, store.ErrStale) {
		t.Fatalf("archive with a live schedule = %v; want ErrStale", err)
	}
	if got := stateJob(t, s, "files:main", nil, "busy"); got.ArchivedAt != nil || got.Bindings != 1 {
		t.Fatalf("job = %+v; want live with one binding", got)
	}
}
