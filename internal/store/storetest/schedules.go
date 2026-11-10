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

func newSchedule(id, jobID, name string, next time.Time) store.Schedule {
	return store.Schedule{
		ID: id, JobID: jobID, Name: name, Cron: "*/5 * * * *", Timezone: "UTC", Options: json.RawMessage(`{"env":"dev"}`),
		Enabled: true, Misfire: store.MisfireRunOnce, NextFireAt: &next,
	}
}

func testSchedules(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	job, _ := mustJob(t, s, "sched-job")
	next := t0.Add(5 * time.Minute)

	created, err := s.CreateSchedule(ctx, newSchedule("s1", job.ID, "nightly", next), t0)
	if err != nil || created.Version != 1 || !created.NextFireAt.Equal(next) {
		t.Fatalf("CreateSchedule = %+v, %v", created, err)
	}
	got, err := s.GetSchedule(ctx, "s1")
	if err != nil || got.Name != "nightly" || !got.Enabled || got.Misfire != store.MisfireRunOnce || string(got.Options) == "" || got.LastFireAt != nil {
		t.Fatalf("GetSchedule = %+v, %v", got, err)
	}

	// Validation, missing job, duplicate name and a missing actor.
	bad := newSchedule("s2", job.ID, "x", next)
	bad.Misfire = "later"
	if _, err := s.CreateSchedule(ctx, bad, t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad misfire: %v; want ErrInvalid", err)
	}
	if _, err := s.CreateSchedule(ctx, newSchedule("s2", "no-such-job", "x", next), t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing job: %v; want ErrNotFound", err)
	}
	if _, err := s.CreateSchedule(ctx, newSchedule("s3", job.ID, "nightly", next), t0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v; want ErrConflict", err)
	}
	if _, err := s.CreateSchedule(context.Background(), newSchedule("s4", job.ID, "other", next), t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("no actor: %v; want ErrInvalid", err)
	}
	if _, err := s.GetSchedule(ctx, "s4"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused create left a row: %v", err)
	}

	// Update is a CAS on the version.
	upd := created
	upd.Cron, upd.Enabled, upd.NextFireAt = "0 3 * * *", false, nil
	upd.Options = json.RawMessage(`{"env":"prod"}`)
	u, err := s.UpdateSchedule(ctx, upd, 1, t0.Add(time.Minute))
	if err != nil || u.Version != 2 || u.Enabled || u.NextFireAt != nil || u.Cron != "0 3 * * *" {
		t.Fatalf("UpdateSchedule = %+v, %v", u, err)
	}
	if _, err := s.UpdateSchedule(ctx, upd, 1, t0); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale update: %v; want ErrStale", err)
	}
	missing := upd
	missing.ID = "nope"
	if _, err := s.UpdateSchedule(ctx, missing, 1, t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing: %v; want ErrNotFound", err)
	}

	// Listing: by job, keyset by id.
	for _, id := range []string{"s5", "s6"} {
		if _, err := s.CreateSchedule(ctx, newSchedule(id, job.ID, "n-"+id, next), t0); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListSchedules(ctx, job.ID, "", 2)
	if err != nil || len(page) != 2 || page[0].ID != "s1" || page[1].ID != "s5" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	if rest, _ := s.ListSchedules(ctx, "", "s5", 10); len(rest) != 1 || rest[0].ID != "s6" {
		t.Fatalf("second page = %+v", rest)
	}
	if none, _ := s.ListSchedules(ctx, "other-job", "", 10); len(none) != 0 {
		t.Fatalf("other job lists %d schedules", len(none))
	}

	// Delete, audited.
	if err := s.DeleteSchedule(ctx, "s6", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSchedule(ctx, "s6", t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v; want ErrNotFound", err)
	}
	if err := s.DeleteSchedule(context.Background(), "s5", t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("delete without actor: %v; want ErrInvalid", err)
	}
	evs, err := s.ListAuditEvents(ctx, store.AuditFilter{TargetType: store.AuditTargetSchedule}, 50)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]int{}
	for _, e := range evs {
		actions[e.Action]++
		if string(e.Details) == "" || !json.Valid(e.Details) || contains(string(e.Details), "prod") {
			t.Fatalf("audit details %q", e.Details)
		}
	}
	if actions[store.ActionScheduleCreate] != 3 || actions[store.ActionScheduleUpdate] != 1 || actions[store.ActionScheduleDelete] != 1 {
		t.Fatalf("audit actions = %v", actions)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func fire(s store.Store, f store.ScheduleFire) error {
	return s.WithTx(context.Background(), func(tx store.Tx) error { return tx.FireSchedule(context.Background(), f) })
}

func testScheduleFire(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	job, _ := mustJob(t, s, "fire-job")
	due := t0.Add(time.Minute)
	for i, id := range []string{"a", "b", "c"} {
		sc := newSchedule(id, job.ID, id, due.Add(time.Duration(i)*time.Minute))
		if id == "c" {
			sc.Enabled, sc.NextFireAt = false, nil
		}
		if _, err := s.CreateSchedule(ctx, sc, t0); err != nil {
			t.Fatal(err)
		}
	}
	if d, err := s.ListDueSchedules(ctx, t0, 10); err != nil || len(d) != 0 {
		t.Fatalf("nothing is due yet: %+v, %v", d, err)
	}
	d, err := s.ListDueSchedules(ctx, due.Add(90*time.Second), 10)
	if err != nil || len(d) != 2 || d[0].ID != "a" || d[1].ID != "b" {
		t.Fatalf("due = %+v, %v; want a, b (disabled c excluded), oldest first", d, err)
	}
	if one, _ := s.ListDueSchedules(ctx, due.Add(time.Hour), 1); len(one) != 1 {
		t.Fatalf("limit ignored: %d", len(one))
	}

	next := due.Add(5 * time.Minute)
	f := store.ScheduleFire{ID: "a", Expected: due, ExpectedVersion: 1, Next: next, Fired: due.Add(time.Second), RunID: "run-1"}
	if err := fire(s, f); err != nil {
		t.Fatalf("FireSchedule: %v", err)
	}
	got, _ := s.GetSchedule(ctx, "a")
	if !got.NextFireAt.Equal(next) || got.LastRunID != "run-1" || got.LastFireAt == nil || got.Version != 2 || got.LastError != "" {
		t.Fatalf("after fire = %+v", got)
	}
	// Firing the same time again is refused: the exactly-once guard.
	if err := fire(s, f); !errors.Is(err, store.ErrStale) {
		t.Fatalf("second fire: %v; want ErrStale", err)
	}
	// A failure records the reason and no run.
	f = store.ScheduleFire{ID: "b", Expected: due.Add(time.Minute), ExpectedVersion: 1, Next: next, Error: "options are invalid"}
	if err := fire(s, f); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetSchedule(ctx, "b"); got.LastError != "options are invalid" {
		t.Fatalf("after failed fire = %+v", got)
	}
	// An edit between the read and the fire cancels the fire, and a fire cancels
	// an edit made from the version before it.
	if _, err := s.UpdateSchedule(ctx, func() store.Schedule { g, _ := s.GetSchedule(ctx, "b"); g.Misfire = store.MisfireSkip; return g }(), 2, t0); err != nil {
		t.Fatal(err)
	}
	if err := fire(s, store.ScheduleFire{ID: "b", Expected: next, ExpectedVersion: 2, Next: next.Add(time.Hour)}); !errors.Is(err, store.ErrStale) {
		t.Fatalf("fire after an edit: %v; want ErrStale", err)
	}
	if err := fire(s, store.ScheduleFire{ID: "a", Expected: next, ExpectedVersion: 2, Next: next.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	stale, _ := s.GetSchedule(ctx, "a")
	stale.Version = 2
	if _, err := s.UpdateSchedule(ctx, stale, 2, t0); !errors.Is(err, store.ErrStale) {
		t.Fatalf("update from the pre-fire version: %v; want ErrStale", err)
	}
	// A skipped fire keeps the last real run.
	if kept, _ := s.GetSchedule(ctx, "a"); kept.LastRunID != "run-1" || kept.LastFireAt == nil {
		t.Fatalf("last run lost: %+v", kept)
	}
	// A disabled schedule cannot be fired.
	if err := fire(s, store.ScheduleFire{ID: "c", Expected: due, Next: next}); !errors.Is(err, store.ErrStale) {
		t.Fatalf("fire disabled: %v; want ErrStale", err)
	}
}

// Two controllers firing the same time: one wins.
func testScheduleFireRace(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	job, _ := mustJob(t, s, "race-job")
	due := t0.Add(time.Minute)
	if _, err := s.CreateSchedule(ctx, newSchedule("r", job.ID, "r", due), t0); err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = fire(s, store.ScheduleFire{ID: "r", Expected: due, ExpectedVersion: 1, Next: due.Add(time.Hour), Fired: due, RunID: "run"})
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, store.ErrStale):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d fires won; want exactly 1", wins)
	}
}

// testTriggersByProject: schedules and webhooks are listed by project and the
// projects below it, never by a mere name prefix, and their audit events carry
// the project of their job.
func testTriggersByProject(t *testing.T, s store.Store) {
	ctx := actorCtx()
	migrated(t, s)
	mustProject(t, s, "pa", "pa")
	mustProject(t, s, "pab", "pa/b")
	mustProject(t, s, "pax", "pa-x") // shares the prefix "pa", is not below it
	job := func(project, name string) store.Job {
		t.Helper()
		j, _, err := s.CreateJob(ctx, store.NewJob{ProjectID: project, Name: name, Spec: json.RawMessage(specJSON), Checksum: "sum-" + name}, t0)
		if err != nil {
			t.Fatalf("CreateJob %s: %v", name, err)
		}
		return j
	}
	ja, jb, jx := job("pa", "ja"), job("pab", "jb"), job("pax", "jx")
	next := t0.Add(time.Minute)
	for _, c := range []struct{ id, job string }{{"tsa", ja.ID}, {"tsb", jb.ID}, {"tsx", jx.ID}} {
		if _, err := s.CreateSchedule(ctx, newSchedule(c.id, c.job, "n", next), t0); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ id, job, hex string }{{"twa", ja.ID, "d"}, {"twb", jb.ID, "e"}, {"twx", jx.ID, "f"}} {
		if _, err := s.CreateWebhook(ctx, newWebhook(c.id, c.job, "hook", hashOf(c.hex)), t0); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(rows []store.Schedule) (out []string) {
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	wids := func(rows []store.Webhook) (out []string) {
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	for _, c := range []struct {
		project string
		want    []string
	}{{"pa", []string{"a", "b"}}, {"pa/b", []string{"b"}}, {"pa-x", []string{"x"}}, {"p", nil}, {"nope", nil}, {"", nil}} {
		gs, err := s.ListSchedulesByProject(ctx, c.project, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		gw, err := s.ListWebhooksByProject(ctx, c.project, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		var ws, ww []string
		for _, w := range c.want {
			ws, ww = append(ws, "ts"+w), append(ww, "tw"+w)
		}
		if got := ids(gs); !equalStrings(got, ws) {
			t.Errorf("schedules of %q = %v; want %v", c.project, got, ws)
		}
		if got := wids(gw); !equalStrings(got, ww) {
			t.Errorf("webhooks of %q = %v; want %v", c.project, got, ww)
		}
	}
	// Keyset paging inside a project.
	if rest, err := s.ListSchedulesByProject(ctx, "pa", "tsa", 10); err != nil || !equalStrings(ids(rest), []string{"tsb"}) {
		t.Errorf("after tsa = %v, %v", ids(rest), err)
	}

	// The audit events of these writes name the project of their job.
	if err := s.DeleteSchedule(ctx, "tsb", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeWebhook(ctx, "twb", t0); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{store.AuditTargetSchedule, store.AuditTargetWebhook} {
		evs, err := s.ListAuditEvents(ctx, store.AuditFilter{TargetType: target}, 100)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"tsa": "pa", "tsb": "pa/b", "tsx": "pa-x", "twa": "pa", "twb": "pa/b", "twx": "pa-x"}
		seen := 0
		for _, e := range evs {
			if w, ok := want[e.Target]; ok {
				seen++
				if e.ProjectPath != w {
					t.Errorf("%s event of %s has project %q; want %q", e.Action, e.Target, e.ProjectPath, w)
				}
			}
		}
		if seen < 3 {
			t.Errorf("%s: saw %d events of the test's rows; want at least 3", target, seen)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
