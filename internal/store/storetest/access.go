package storetest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
)

func testProjectChain(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	mustProject(t, s, "c-a", "a")
	mustProject(t, s, "c-ab", "a/b")
	mustProject(t, s, "c-abc", "a/b/c")
	mustProject(t, s, "c-a2", "a-2") // a sibling that shares the prefix "a"
	mustProject(t, s, "c-ab2", "a/bb")

	chain, err := s.ListProjectChain(ctx, "a/b/c")
	if err != nil || fmt.Sprint(paths(chain)) != "[a a/b a/b/c]" {
		t.Fatalf("chain(a/b/c) = %v, %v", paths(chain), err)
	}
	if chain, _ := s.ListProjectChain(ctx, "a"); fmt.Sprint(paths(chain)) != "[a]" {
		t.Fatalf("chain(a) = %v", paths(chain))
	}
	if chain, _ := s.ListProjectChain(ctx, "a/bb"); fmt.Sprint(paths(chain)) != "[a a/bb]" {
		t.Fatalf("chain(a/bb) = %v; a/b is a sibling, not an ancestor", paths(chain))
	}
	if chain, _ := s.ListProjectChain(ctx, "a/b/x"); fmt.Sprint(paths(chain)) != "[a a/b]" {
		t.Fatalf("chain(a/b/x) = %v; the existing ancestors of a missing path", paths(chain))
	}
	if chain, _ := s.ListProjectChain(ctx, "nope"); len(chain) != 0 {
		t.Fatalf("chain(nope) = %v", paths(chain))
	}
}

func testProjectAccessRules(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	p := mustProject(t, s, "ar-1", "team")
	if len(p.AccessRules) != 0 {
		t.Fatalf("new project rules = %v", p.AccessRules)
	}
	rules := []store.AccessRule{{Role: "runner", Rule: `'ops' in claims.groups`}, {Role: "admin", Rule: `user.email == 'a@b.c'`}}
	got, err := s.SetProjectAccessRules(ctx, p.ID, rules, 1, t0)
	if err != nil || got.Version != 2 || len(got.AccessRules) != 2 || got.AccessRules[1].Role != "admin" {
		t.Fatalf("SetProjectAccessRules = %+v, %v", got, err)
	}
	stored, _ := s.GetProject(context.Background(), p.ID)
	if len(stored.AccessRules) != 2 || stored.AccessRules[0].Rule != rules[0].Rule || stored.Version != 2 {
		t.Fatalf("stored = %+v", stored)
	}
	if chain, _ := s.ListProjectChain(context.Background(), "team"); len(chain[0].AccessRules) != 2 {
		t.Fatalf("chain rules = %+v", chain)
	}
	if _, err := s.SetProjectAccessRules(ctx, p.ID, rules, 1, t0); !errors.Is(err, store.ErrStale) {
		t.Errorf("stale: %v; want ErrStale", err)
	}
	if _, err := s.SetProjectAccessRules(ctx, "nope", rules, 1, t0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing: %v; want ErrNotFound", err)
	}
	if _, err := s.SetProjectAccessRules(context.Background(), p.ID, rules, 2, t0); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("no actor: %v; want ErrInvalid", err)
	}
	for name, bad := range map[string][]store.AccessRule{
		"unknown role": {{Role: "root", Rule: "true"}},
		"empty rule":   {{Role: "viewer", Rule: ""}},
		"huge rule":    {{Role: "viewer", Rule: strings.Repeat("a", store.MaxAccessRuleBytes+1)}},
		"too many":     make([]store.AccessRule, store.MaxAccessRules+1),
	} {
		if _, err := s.SetProjectAccessRules(ctx, p.ID, bad, 2, t0); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: %v; want ErrInvalid", name, err)
		}
	}
	// Clearing the rules.
	if got, err := s.SetProjectAccessRules(ctx, p.ID, nil, 2, t0); err != nil || len(got.AccessRules) != 0 {
		t.Errorf("clear = %+v, %v", got, err)
	}

	// The audit event names counts and roles, not expressions.
	evs, err := s.ListAuditEvents(context.Background(), store.AuditFilter{TargetType: store.AuditTargetProject, Target: p.ID}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found int
	for _, e := range evs {
		if e.Action != store.ActionProjectAccessRulesUpdate {
			continue
		}
		found++
		if e.ProjectPath != "team" || strings.Contains(string(e.Details), "claims") || strings.Contains(string(e.Details), "email") {
			t.Errorf("event = %+v", e)
		}
	}
	if found != 2 {
		t.Errorf("%d access rule events; want 2", found)
	}
}

func testJobAccessRules(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	job, _ := mustJob(t, s, "ja")

	if a, err := s.GetJobAccess(ctx, job.ID); err != nil || a.Version != 0 || len(a.Rules) != 0 {
		t.Fatalf("no rules yet = %+v, %v", a, err)
	}
	rules := []store.AccessRule{{Role: "runner", Rule: `'deployers' in claims.groups`}}
	a, err := s.SetJobAccessRules(ctx, job.ID, rules, 0, t0)
	if err != nil || a.Version != 1 {
		t.Fatalf("create = %+v, %v", a, err)
	}
	if _, err := s.SetJobAccessRules(ctx, job.ID, rules, 0, t0); !errors.Is(err, store.ErrStale) {
		t.Errorf("second create: %v; want ErrStale", err)
	}
	a, err = s.SetJobAccessRules(ctx, job.ID, append(rules, store.AccessRule{Role: "viewer", Rule: "true"}), 1, t0)
	if err != nil || a.Version != 2 {
		t.Fatalf("update = %+v, %v", a, err)
	}
	if _, err := s.SetJobAccessRules(ctx, job.ID, rules, 1, t0); !errors.Is(err, store.ErrStale) {
		t.Errorf("stale update: %v; want ErrStale", err)
	}
	got, _ := s.GetJobAccess(ctx, job.ID)
	if got.Version != 2 || len(got.Rules) != 2 {
		t.Errorf("stored = %+v", got)
	}
	if _, err := s.SetJobAccessRules(ctx, job.ID, []store.AccessRule{{Role: "admin", Rule: "true"}}, 2, t0); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("admin on a job: %v; want ErrInvalid (jobs grant up to editor)", err)
	}
	if _, err := s.SetJobAccessRules(ctx, "nope", rules, 0, t0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing job: %v; want ErrNotFound", err)
	}
	evs, _ := s.ListAuditEvents(context.Background(), store.AuditFilter{TargetType: store.AuditTargetJob, Target: job.ID}, 10)
	if len(evs) != 2 || evs[0].ProjectPath != "default" {
		t.Errorf("events = %+v", evs)
	}
}

func testAccessRaces(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	job, _ := mustJob(t, s, "race")
	p := mustProject(t, s, "arr", "racep")
	rules := []store.AccessRule{{Role: "viewer", Rule: "true"}}

	var jobWins, projWins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := s.SetJobAccessRules(ctx, job.ID, rules, 0, t0)
			if err == nil {
				jobWins.Add(1)
			} else if !errors.Is(err, store.ErrStale) {
				t.Errorf("job rules: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			_, err := s.SetProjectAccessRules(ctx, p.ID, rules, 1, t0)
			if err == nil {
				projWins.Add(1)
			} else if !errors.Is(err, store.ErrStale) {
				t.Errorf("project rules: %v", err)
			}
		}()
	}
	wg.Wait()
	if jobWins.Load() != 1 || projWins.Load() != 1 {
		t.Errorf("job winners %d, project winners %d; want one each", jobWins.Load(), projWins.Load())
	}
}

func testAuditSubtreeAndNames(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	at := func(id, project string) store.AuditEvent {
		return store.AuditEvent{ID: id, At: t0, Actor: "user:u1", ActorName: "u1@example.org", Action: "project.create", TargetType: "project", Target: id, ProjectPath: project, Details: []byte(`{}`)}
	}
	for _, e := range []store.AuditEvent{at("e1", "infra"), at("e2", "infra/web"), at("e3", "infra/web/eu"), at("e4", "infrastructure"), at("e5", "infra-2"), at("e6", "")} {
		if err := s.AppendAuditEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(f store.AuditFilter) string {
		evs, err := s.ListAuditEvents(ctx, f, 50)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range evs {
			out = append(out, e.ID)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	if got := ids(store.AuditFilter{Project: "infra"}); got != "e1,e2,e3" {
		t.Errorf("subtree(infra) = %s; infrastructure and infra-2 are siblings", got)
	}
	if got := ids(store.AuditFilter{Project: "infra/web"}); got != "e2,e3" {
		t.Errorf("subtree(infra/web) = %s", got)
	}
	if got := ids(store.AuditFilter{Project: "nope"}); got != "" {
		t.Errorf("subtree(nope) = %s", got)
	}
	if got := ids(store.AuditFilter{}); got != "e1,e2,e3,e4,e5,e6" {
		t.Errorf("no project = %s; want every event", got)
	}
	evs, _ := s.ListAuditEvents(ctx, store.AuditFilter{Project: "infra/web/eu"}, 5)
	if len(evs) != 1 || evs[0].ActorName != "u1@example.org" {
		t.Errorf("actor name = %+v", evs)
	}

	// The context's display name is stored for events of its actor.
	actx := store.WithAudit(ctx, store.AuditMeta{Actor: "user:u2", ActorName: "u2@example.org"})
	if err := s.AppendAuditEvent(actx, store.AuditEvent{ID: "e7", At: t0, Actor: "user:u2", Action: "x", TargetType: "x", Target: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAuditEvent(actx, store.AuditEvent{ID: "e8", At: t0, Actor: "system:other", Action: "x", TargetType: "x", Target: "e8"}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"e7": "u2@example.org", "e8": ""} {
		got, _ := s.ListAuditEvents(ctx, store.AuditFilter{Target: map[string]string{"e7": "x", "e8": "e8"}[id]}, 5)
		if len(got) != 1 || got[0].ActorName != want {
			t.Errorf("event %s actor name = %+v; want %q", id, got, want)
		}
	}
}

func testJobAudit(t *testing.T, s store.Store) {
	migrated(t, s)
	actx := store.WithAudit(context.Background(), store.AuditMeta{Actor: "user:u1", ActorName: "u1@example.org"})
	nj := store.NewJob{ProjectID: store.DefaultProjectID, Name: "audited", Spec: []byte(specJSON), Checksum: "c1"}
	job, _, err := s.CreateJob(actx, nj, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateJobVersion(actx, job.ID, []byte(specJSON), "c2", 1, t0); err != nil {
		t.Fatal(err)
	}
	// A stale version change writes no event.
	if _, err := s.CreateJobVersion(actx, job.ID, []byte(specJSON), "c3", 1, t0); !errors.Is(err, store.ErrStale) {
		t.Fatal(err)
	}
	// Without an actor nothing is audited (imports, tests).
	if _, _, err := s.CreateJob(context.Background(), store.NewJob{ProjectID: store.DefaultProjectID, Name: "quiet", Spec: []byte(specJSON), Checksum: "c"}, t0); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ListAuditEvents(context.Background(), store.AuditFilter{TargetType: store.AuditTargetJob}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("events = %+v; want job.create and job.version.create", evs)
	}
	for _, e := range evs {
		if e.Target != job.ID || e.ProjectPath != "default" || e.Actor != "user:u1" || e.ActorName != "u1@example.org" {
			t.Errorf("event = %+v", e)
		}
	}
}

func testProjectNodeScope(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	p := mustProject(t, s, "ns-1", "scoped")
	if p.NodeScope != "" {
		t.Fatalf("new project scope = %q", p.NodeScope)
	}
	got, err := s.SetProjectNodeScope(ctx, p.ID, `'prod' in node.tags`, 1, t0)
	if err != nil || got.Version != 2 || got.NodeScope != `'prod' in node.tags` {
		t.Fatalf("SetProjectNodeScope = %+v, %v", got, err)
	}
	stored, _ := s.GetProject(context.Background(), p.ID)
	if stored.NodeScope != got.NodeScope || stored.Version != 2 {
		t.Fatalf("stored = %+v", stored)
	}
	if chain, _ := s.ListProjectChain(context.Background(), "scoped"); chain[0].NodeScope != got.NodeScope {
		t.Fatalf("chain scope = %+v", chain)
	}
	if _, err := s.SetProjectNodeScope(ctx, p.ID, "true", 1, t0); !errors.Is(err, store.ErrStale) {
		t.Errorf("stale: %v", err)
	}
	if _, err := s.SetProjectNodeScope(ctx, "nope", "true", 1, t0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := s.SetProjectNodeScope(context.Background(), p.ID, "true", 2, t0); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("no actor: %v", err)
	}
	if _, err := s.SetProjectNodeScope(ctx, p.ID, strings.Repeat("a", store.MaxNodeScopeBytes+1), 2, t0); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("huge scope: %v", err)
	}
	if got, err := s.SetProjectNodeScope(ctx, p.ID, "", 2, t0); err != nil || got.NodeScope != "" {
		t.Errorf("clear = %+v, %v", got, err)
	}
	evs, _ := s.ListAuditEvents(context.Background(), store.AuditFilter{TargetType: store.AuditTargetProject, Target: p.ID}, 10)
	var n int
	for _, e := range evs {
		if e.Action == store.ActionProjectNodeScopeUpdate {
			n++
			if e.ProjectPath != "scoped" {
				t.Errorf("event = %+v", e)
			}
		}
	}
	if n != 2 {
		t.Errorf("%d node scope events; want 2", n)
	}
}
