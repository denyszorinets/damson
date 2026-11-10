package storetest

import (
	"context"
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
