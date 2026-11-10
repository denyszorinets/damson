package storetest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// testProjectCounts: jobs, failed runs since the cutoff and running runs are
// counted per project; older failures, other statuses and archived jobs are not.
func testProjectCounts(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	now := time.Now().UTC()
	cutoff := now.Add(-24 * time.Hour)

	job, tree := mustRun(t, s, "stats-a") // a run that stays pending: not counted
	_ = tree
	finish := func(name string, status store.RunStatus, at time.Time) {
		_, tr := mustRun(t, s, name)
		if status == store.RunPending {
			return
		}
		if _, err := s.UpdateRunStatus(ctx, tr.ID, tr.Version, status, at); err != nil {
			t.Fatal(err)
		}
	}
	finish("stats-failed-new", store.RunFailed, now.Add(-time.Hour))
	finish("stats-failed-old", store.RunFailed, now.Add(-48*time.Hour))
	finish("stats-ok", store.RunSucceeded, now.Add(-time.Hour))
	finish("stats-running", store.RunRunning, now.Add(-time.Minute))

	got, err := s.ProjectCounts(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	c := got[job.ProjectID] // mustRun puts every job in the default project
	if c.Jobs != 5 || c.Failed24h != 1 || c.Running != 1 {
		t.Errorf("counts = %+v; want 5 jobs, 1 failed in 24h, 1 running", c)
	}
	if _, ok := got["no-such-project"]; ok {
		t.Error("a project that does not exist has counts")
	}
}

// testJobRunStats: a job's last success, last failure, last run and the
// duration of its newest finished run come from its runs; a job that never ran
// and an archived job have none.
func testJobRunStats(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	job, ver := mustJob(t, s, "last-runs")
	idle, _ := mustJob(t, s, "never-ran")
	base := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	run := func(start time.Time, final store.RunStatus, took time.Duration) {
		t.Helper()
		tree, err := s.CreateRun(ctx, store.NewRun{
			JobID: job.ID, JobVersionID: ver.ID, Trigger: json.RawMessage(`{"type":"manual"}`),
			Options: json.RawMessage(`{}`), Steps: []store.NewStepInstance{{Path: "main/a", Plugin: "shell", Input: json.RawMessage(`{}`), Execution: execSpec("default")}},
		}, start)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.UpdateRunStatus(ctx, tree.ID, tree.Version, store.RunRunning, start)
		if err != nil {
			t.Fatal(err)
		}
		if final == store.RunRunning {
			return
		}
		if _, err := s.UpdateRunStatus(ctx, r.ID, r.Version, final, start.Add(took)); err != nil {
			t.Fatal(err)
		}
	}
	run(base, store.RunSucceeded, 30*time.Second)
	run(base.Add(time.Minute), store.RunFailed, 10*time.Second)
	run(base.Add(2*time.Minute), store.RunSucceeded, 45*time.Second)
	run(base.Add(3*time.Minute), store.RunRunning, 0)

	got, err := s.JobRunStats(ctx, store.DefaultProjectID)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := got[job.ID]
	if !ok {
		t.Fatalf("no stats for %s: %+v", job.ID, got)
	}
	if st.LastStatus != store.RunRunning {
		t.Errorf("LastStatus = %s; want running", st.LastStatus)
	}
	if st.LastSuccessAt == nil || !st.LastSuccessAt.Equal(base.Add(2*time.Minute+45*time.Second)) {
		t.Errorf("LastSuccessAt = %v; want the newest succeeded run's finish", st.LastSuccessAt)
	}
	if st.LastFailureAt == nil || !st.LastFailureAt.Equal(base.Add(time.Minute+10*time.Second)) {
		t.Errorf("LastFailureAt = %v; want the failed run's finish", st.LastFailureAt)
	}
	if st.LastDuration == nil || *st.LastDuration != 45*time.Second {
		t.Errorf("LastDuration = %v; want 45s, the newest finished run", st.LastDuration)
	}
	if _, ok := got[idle.ID]; ok {
		t.Error("a job that never ran has stats")
	}
	if other, err := s.JobRunStats(ctx, "no-such-project"); err != nil || len(other) != 0 {
		t.Errorf("another project = %v, %v; want none", other, err)
	}
}
