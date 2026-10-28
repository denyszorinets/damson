package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/denyszorinets/damson/internal/store"
)

func testQueryRuns(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	jobA, verA := mustJob(t, s, "qa")
	jobB, verB := mustJob(t, s, "qb")
	var all, forA []string
	for i := range 5 {
		job, ver := jobA, verA
		if i%2 == 1 {
			job, ver = jobB, verB
		}
		tree, err := s.CreateRun(ctx, store.NewRun{JobID: job.ID, JobVersionID: ver.ID}, t0)
		if err != nil {
			t.Fatal(err)
		}
		all = append([]string{tree.ID}, all...)
		if job.ID == jobA.ID {
			forA = append([]string{tree.ID}, forA...)
		}
	}
	if _, err := s.UpdateRunStatus(ctx, all[0], 1, store.RunRunning, t0); err != nil {
		t.Fatal(err)
	}
	page := func(f store.RunFilter, limit int) []string {
		t.Helper()
		var got []string
		cursor := ""
		for range 10 {
			p, err := s.QueryRuns(ctx, f, cursor, limit)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range p.Runs {
				got = append(got, r.ID)
			}
			if p.NextCursor == "" {
				break
			}
			cursor = p.NextCursor
		}
		return got
	}
	for _, tc := range []struct {
		name string
		f    store.RunFilter
		want []string
	}{
		{"all", store.RunFilter{}, all},
		{"job", store.RunFilter{JobID: jobA.ID}, forA},
		{"status", store.RunFilter{Status: store.RunRunning}, all[:1]},
		{"job and status", store.RunFilter{JobID: jobB.ID, Status: store.RunRunning}, nil},
		{"no match", store.RunFilter{Status: store.RunFailed}, nil},
	} {
		if got := page(tc.f, 2); !slices.Equal(got, tc.want) {
			t.Errorf("QueryRuns %s = %v; want %v", tc.name, got, tc.want)
		}
	}
}

func testCreateRunWithID(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	job, ver := mustJob(t, s, "withid")
	id, err := store.NewID()
	if err != nil {
		t.Fatal(err)
	}
	tree, err := s.CreateRun(ctx, store.NewRun{ID: id, JobID: job.ID, JobVersionID: ver.ID}, t0)
	if err != nil || tree.ID != id {
		t.Fatalf("CreateRun with id = %q, %v; want %q", tree.ID, err, id)
	}
	if _, err := s.GetRun(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctx, store.NewRun{ID: id, JobID: job.ID, JobVersionID: ver.ID}, t0); !errors.Is(err, store.ErrConflict) {
		t.Errorf("CreateRun with a duplicate id: %v; want ErrConflict", err)
	}
	if _, err := s.CreateRun(ctx, store.NewRun{ID: "not-a-uuid", JobID: job.ID, JobVersionID: ver.ID}, t0); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("CreateRun with a bad id: %v; want ErrInvalid", err)
	}
}
