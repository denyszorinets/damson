package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
)

func mustProject(t *testing.T, s store.Store, id, path string) store.Project {
	t.Helper()
	p, err := s.CreateProject(actorCtx(), id, path, "", t0)
	if err != nil {
		t.Fatalf("CreateProject %s: %v", path, err)
	}
	return p
}

func testProjects(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	p, err := s.GetProjectByPath(ctx, store.DefaultProjectPath)
	if err != nil || p.ID != store.DefaultProjectID || p.ParentID != "" || p.Version != 1 {
		t.Fatalf("default project = %+v, %v", p, err)
	}
	for _, junk := range []string{"", "a\x00b", "Upper", "a//b", "_inventory"} {
		if _, err := s.GetProjectByPath(ctx, junk); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetProjectByPath(%q): %v; want ErrNotFound", junk, err)
		}
		if chain, err := s.ListProjectChain(ctx, junk); err != nil || len(chain) != 0 {
			t.Fatalf("ListProjectChain(%q) = %v, %v; want empty", junk, chain, err)
		}
	}
	if _, err := s.GetProjectByPath(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing project: %v; want ErrNotFound", err)
	}
	if got, err := s.GetProject(ctx, p.ID); err != nil || got.ID != p.ID || got.Path != p.Path || got.Version != p.Version {
		t.Fatalf("GetProject = %+v, %v", got, err)
	}
	if list, err := s.ListProjects(ctx); err != nil || len(list) != 1 {
		t.Fatalf("ListProjects = %+v, %v", list, err)
	}
}

func paths(ps []store.Project) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Path
	}
	return out
}

func testProjectTree(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)

	infra := mustProject(t, s, "p-infra", "infra")
	child := mustProject(t, s, "p-infra-web", "infra/web")
	if child.ParentID != infra.ID || child.Version != 1 {
		t.Fatalf("child = %+v", child)
	}
	mustProject(t, s, "p-infra-web-eu", "infra/web/eu")
	mustProject(t, s, "p-infrastructure", "infrastructure") // shares the prefix "infra" but is no descendant
	mustProject(t, s, "p-infra2", "infra-2")

	got, err := s.GetProjectByPath(context.Background(), "infra/web")
	if err != nil || got.ID != child.ID || got.ParentID != infra.ID {
		t.Fatalf("GetProjectByPath = %+v, %v", got, err)
	}
	sub, err := s.ListProjectSubtree(context.Background(), "infra")
	if err != nil || fmt.Sprint(paths(sub)) != "[infra infra/web infra/web/eu]" {
		t.Fatalf("subtree(infra) = %v, %v", paths(sub), err)
	}
	if sub, _ := s.ListProjectSubtree(context.Background(), "infra/web"); fmt.Sprint(paths(sub)) != "[infra/web infra/web/eu]" {
		t.Fatalf("subtree(infra/web) = %v", paths(sub))
	}
	if sub, _ := s.ListProjectSubtree(context.Background(), "nope"); len(sub) != 0 {
		t.Fatalf("subtree(nope) = %v", paths(sub))
	}
	if all, _ := s.ListProjects(context.Background()); fmt.Sprint(paths(all)) != "[default infra infra-2 infra/web infra/web/eu infrastructure]" {
		t.Fatalf("ListProjects = %v", paths(all))
	}

	// Validation and conflicts.
	for _, bad := range []string{"", "Infra", "a//b", "/a", "a/", "_inventory", "a/_b", "a b", "a/b/c/d/e/f/g/h/i"} {
		if _, err := s.CreateProject(ctx, "x", bad, "", t0); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("path %q: %v; want ErrInvalid", bad, err)
		}
	}
	if _, err := s.CreateProject(ctx, "dup", "infra", "", t0); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate path: %v; want ErrConflict", err)
	}
	if _, err := s.CreateProject(ctx, "orphan", "ghost/child", "", t0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing parent: %v; want ErrNotFound", err)
	}
	if _, err := s.CreateProject(context.Background(), "na", "noactor", "", t0); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("no actor: %v; want ErrInvalid", err)
	}

	// Update is a CAS on version.
	up, err := s.UpdateProject(ctx, infra.ID, "shared infra", 1, t0)
	if err != nil || up.Version != 2 || up.Description != "shared infra" || up.Path != "infra" {
		t.Fatalf("UpdateProject = %+v, %v", up, err)
	}
	if _, err := s.UpdateProject(ctx, infra.ID, "late", 1, t0); !errors.Is(err, store.ErrStale) {
		t.Errorf("stale update: %v; want ErrStale", err)
	}
	if _, err := s.UpdateProject(ctx, "nope", "x", 1, t0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing update: %v; want ErrNotFound", err)
	}
	if again, _ := s.GetProject(context.Background(), infra.ID); again.Description != "shared infra" || again.Version != 2 {
		t.Errorf("stored project = %+v", again)
	}

	// Audit: one event per write, carrying the path as project scope.
	evs, err := s.ListAuditEvents(context.Background(), store.AuditFilter{TargetType: store.AuditTargetProject, Target: infra.ID}, 10)
	if err != nil || len(evs) != 2 {
		t.Fatalf("project events = %d, %v", len(evs), err)
	}
	for _, e := range evs {
		if e.ProjectPath != "infra" || e.Actor != "test:actor" {
			t.Errorf("event = %+v", e)
		}
	}
}

func testProjectDelete(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	parent := mustProject(t, s, "pd-a", "a")
	leaf := mustProject(t, s, "pd-b", "a/b")

	if err := s.DeleteProject(ctx, parent.ID, t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("project with a child: %v; want ErrConflict", err)
	}

	if _, _, err := s.CreateJob(context.Background(), store.NewJob{ProjectID: leaf.ID, Name: "j", Spec: []byte(specJSON), Checksum: "c"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, leaf.ID, t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("project with a job: %v; want ErrConflict", err)
	}

	empty := mustProject(t, s, "pd-c", "c")
	if _, err := s.CreateSecret(ctx, "sec1", "c", "db", "password", blob("1"), t0); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, empty.ID, t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("project with a secret: %v; want ErrConflict", err)
	}

	tokenProj := mustProject(t, s, "pd-d", "d")
	if _, err := s.CreateServiceAccount(ctx, store.ServiceAccount{ID: "sa", Name: "ci"}, t0); err != nil {
		t.Fatal(err)
	}
	tok := newToken("tk", "sa", "t", hashOf("x"))
	tok.Project = "d"
	if _, err := s.CreateServiceToken(ctx, tok, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, tokenProj.ID, t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("project with an active token: %v; want ErrConflict", err)
	}
	if err := s.RevokeServiceToken(ctx, "tk", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, tokenProj.ID, t0); err != nil {
		t.Fatalf("after revoke: %v", err)
	}

	free := mustProject(t, s, "pd-e", "e")
	if err := s.DeleteProject(context.Background(), free.ID, t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("no actor: %v; want ErrInvalid", err)
	}
	if err := s.DeleteProject(ctx, free.ID, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, free.ID, t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v; want ErrNotFound", err)
	}
	if _, err := s.GetProjectByPath(context.Background(), "e"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted project still readable: %v", err)
	}
	// The path is free again.
	mustProject(t, s, "pd-e2", "e")
}

func testProjectRaces(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	p := mustProject(t, s, "pr-a", "race")

	var wins, stale atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.UpdateProject(ctx, p.ID, fmt.Sprint("d", i), 1, t0)
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, store.ErrStale):
				stale.Add(1)
			default:
				t.Errorf("update: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || stale.Load() != 7 {
		t.Errorf("wins=%d stale=%d; want exactly one winner", wins.Load(), stale.Load())
	}

	// Two creators of one path: one wins, the other conflicts.
	var created, conflict atomic.Int32
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CreateProject(ctx, fmt.Sprint("pc", i), "same", "", t0)
			switch {
			case err == nil:
				created.Add(1)
			case errors.Is(err, store.ErrConflict):
				conflict.Add(1)
			default:
				t.Errorf("create: %v", err)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 || conflict.Load() != 5 {
		t.Errorf("created=%d conflict=%d", created.Load(), conflict.Load())
	}
}
