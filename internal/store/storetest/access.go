package storetest

import (
	"context"
	"errors"
	"fmt"
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
