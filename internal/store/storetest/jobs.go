package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// specJSON is deliberately not in the canonical key order Postgres jsonb
// would produce, to prove the spec is stored byte for byte.
const specJSON = `{"z": 1,  "a": [1, 2], "name": "demo"}`

func mustJob(t *testing.T, s store.Store, name string) (store.Job, store.JobVersion) {
	t.Helper()
	j, v, err := s.CreateJob(context.Background(), store.NewJob{
		ProjectID: store.DefaultProjectID, Name: name, Spec: json.RawMessage(specJSON), Checksum: "sum-" + name,
	}, t0)
	if err != nil {
		t.Fatalf("CreateJob %s: %v", name, err)
	}
	return j, v
}

func testProjects(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	p, err := s.GetProjectByName(ctx, store.DefaultProjectName)
	if err != nil || p.ID != store.DefaultProjectID {
		t.Fatalf("default project = %+v, %v", p, err)
	}
	if _, err := s.GetProjectByName(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing project: %v; want ErrNotFound", err)
	}
	if list, err := s.ListProjects(ctx); err != nil || len(list) != 1 {
		t.Fatalf("ListProjects = %+v, %v", list, err)
	}
}

func testJobs(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	job, v1 := mustJob(t, s, "deploy")
	if job.CurrentVersion != 1 || job.ManagedBy != "ui" || v1.Version != 1 || v1.JobID != job.ID || !job.CreatedAt.Equal(t0) {
		t.Fatalf("CreateJob = %+v %+v", job, v1)
	}

	gotV, err := s.GetJobVersion(ctx, v1.ID)
	if err != nil || string(gotV.Spec) != specJSON || gotV.Checksum != v1.Checksum {
		t.Fatalf("GetJobVersion = %+v, %v; spec must round-trip byte for byte", gotV, err)
	}
	byNum, err := s.GetJobVersionByNumber(ctx, job.ID, 1)
	if err != nil || byNum.ID != v1.ID {
		t.Fatalf("GetJobVersionByNumber = %+v, %v", byNum, err)
	}
	if got, err := s.GetJobByName(ctx, store.DefaultProjectID, "deploy"); err != nil || got != job {
		t.Fatalf("GetJobByName = %+v, %v; want %+v", got, err, job)
	}

	cases := []struct {
		name string
		nj   store.NewJob
		want error
	}{
		{"duplicate name", store.NewJob{ProjectID: store.DefaultProjectID, Name: "deploy", Spec: json.RawMessage(`{}`), Checksum: "x"}, store.ErrConflict},
		{"unknown project", store.NewJob{ProjectID: "no-such-project", Name: "x", Spec: json.RawMessage(`{}`), Checksum: "x"}, store.ErrNotFound},
		{"empty name", store.NewJob{ProjectID: store.DefaultProjectID, Spec: json.RawMessage(`{}`), Checksum: "x"}, store.ErrInvalid},
		{"empty checksum", store.NewJob{ProjectID: store.DefaultProjectID, Name: "y", Spec: json.RawMessage(`{}`)}, store.ErrInvalid},
		{"bad json", store.NewJob{ProjectID: store.DefaultProjectID, Name: "y", Spec: json.RawMessage(`{`), Checksum: "x"}, store.ErrInvalid},
	}
	for _, tc := range cases {
		if _, _, err := s.CreateJob(ctx, tc.nj, t0); !errors.Is(err, tc.want) {
			t.Errorf("CreateJob %s: %v; want %v", tc.name, err, tc.want)
		}
	}
	if _, err := s.GetJob(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetJob missing: %v", err)
	}
	if _, err := s.GetJobVersionByNumber(ctx, job.ID, 9); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetJobVersionByNumber missing: %v", err)
	}

	// Version CAS: a stale expectation never creates a version.
	v2, err := s.CreateJobVersion(ctx, job.ID, json.RawMessage(`{"v":2}`), "sum2", 1, t0.Add(time.Minute))
	if err != nil || v2.Version != 2 {
		t.Fatalf("CreateJobVersion = %+v, %v", v2, err)
	}
	if _, err := s.CreateJobVersion(ctx, job.ID, json.RawMessage(`{"v":3}`), "sum3", 1, t0); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale CreateJobVersion: %v; want ErrStale", err)
	}
	if _, err := s.CreateJobVersion(ctx, "nope", json.RawMessage(`{}`), "x", 1, t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CreateJobVersion missing job: %v; want ErrNotFound", err)
	}
	cur, _ := s.GetJob(ctx, job.ID)
	if cur.CurrentVersion != 2 || !cur.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("job after version = %+v", cur)
	}
	vs, err := s.ListJobVersions(ctx, job.ID, 0, 10)
	if err != nil || len(vs) != 2 || vs[0].Version != 1 || vs[1].Version != 2 {
		t.Fatalf("ListJobVersions = %+v, %v", vs, err)
	}
	if vs, _ := s.ListJobVersions(ctx, job.ID, 1, 10); len(vs) != 1 || vs[0].Version != 2 {
		t.Fatalf("ListJobVersions after 1 = %+v", vs)
	}
}

func testJobCreateConcurrent(t *testing.T, s store.Store) {
	migrated(t, s)
	mustJob(t, s, "base")
	job, _ := mustJob(t, s, "race")
	res := raceN(8, func(i int) error {
		_, err := s.CreateJobVersion(context.Background(), job.ID, json.RawMessage(`{}`), fmt.Sprint("c", i), 1, t0)
		return err
	})
	if res.won != 1 || res.stale != 7 {
		t.Fatalf("CreateJobVersion winners=%d stale=%d other=%v; want 1 and 7", res.won, res.stale, res.other)
	}
}

func testListJobsPaging(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	for i := range 5 {
		mustJob(t, s, fmt.Sprintf("job-%d", i))
	}
	var seen []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page, err := s.ListJobs(ctx, store.DefaultProjectID, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range page.Jobs {
			seen = append(seen, j.Name)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("paged jobs = %v; want 5 distinct", seen)
	}
}
