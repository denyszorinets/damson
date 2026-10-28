package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

var _ store.JobStore = (*Store)(nil)

func toProject(r gen.Project) (store.Project, error) {
	t, err := parseTime(r.CreatedAt)
	return store.Project{ID: r.ID, Name: r.Name, CreatedAt: t}, err
}

func toJob(r gen.Job) (store.Job, error) {
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return store.Job{}, err
	}
	updated, err := parseTime(r.UpdatedAt)
	if err != nil {
		return store.Job{}, err
	}
	return store.Job{
		ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, ManagedBy: r.ManagedBy,
		CurrentVersion: int(r.CurrentVersion), CreatedAt: created, UpdatedAt: updated,
	}, nil
}

func toJobVersion(r gen.JobVersion) (store.JobVersion, error) {
	t, err := parseTime(r.CreatedAt)
	return store.JobVersion{
		ID: r.ID, JobID: r.JobID, Version: int(r.Version), Spec: json.RawMessage(r.Spec), Checksum: r.Checksum, CreatedAt: t,
	}, err
}

// GetProjectByName implements store.JobStore.
func (s *Store) GetProjectByName(ctx context.Context, name string) (store.Project, error) {
	row, err := s.q.GetProjectByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Project{}, fmt.Errorf("project %q: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return store.Project{}, fmt.Errorf("getting project %q: %w", name, err)
	}
	return toProject(row)
}

// ListProjects implements store.JobStore.
func (s *Store) ListProjects(ctx context.Context) ([]store.Project, error) {
	rows, err := s.q.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	out := make([]store.Project, 0, len(rows))
	for _, r := range rows {
		p, err := toProject(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// CreateJob implements store.JobStore.
func (s *Store) CreateJob(ctx context.Context, nj store.NewJob, now time.Time) (store.Job, store.JobVersion, error) {
	if err := store.ValidateNewJob(nj); err != nil {
		return store.Job{}, store.JobVersion{}, err
	}
	if nj.ManagedBy == "" {
		nj.ManagedBy = "ui"
	}
	jobID, err := store.NewID()
	if err != nil {
		return store.Job{}, store.JobVersion{}, err
	}
	verID, err := store.NewID()
	if err != nil {
		return store.Job{}, store.JobVersion{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.Job{}, store.JobVersion{}, fmt.Errorf("beginning job create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	_, err = q.InsertJob(ctx, gen.InsertJobParams{
		ID: jobID, ProjectID: nj.ProjectID, Name: nj.Name, ManagedBy: nj.ManagedBy,
		CurrentVersion: 1, CreatedAt: fmtTime(now), UpdatedAt: fmtTime(now),
	})
	if isUnique(err) {
		return store.Job{}, store.JobVersion{}, fmt.Errorf("job %q: %w", nj.Name, store.ErrConflict)
	}
	if isForeignKey(err) {
		return store.Job{}, store.JobVersion{}, fmt.Errorf("project %q: %w", nj.ProjectID, store.ErrNotFound)
	}
	if err != nil {
		return store.Job{}, store.JobVersion{}, fmt.Errorf("creating job %q: %w", nj.Name, err)
	}
	if _, err := q.InsertJobVersion(ctx, gen.InsertJobVersionParams{
		ID: verID, JobID: jobID, Version: 1, Spec: string(nj.Spec), Checksum: nj.Checksum, CreatedAt: fmtTime(now),
	}); err != nil {
		return store.Job{}, store.JobVersion{}, fmt.Errorf("creating version 1 of job %q: %w", nj.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return store.Job{}, store.JobVersion{}, fmt.Errorf("committing job create: %w", err)
	}
	n := store.NormalizeTime(now)
	job := store.Job{ID: jobID, ProjectID: nj.ProjectID, Name: nj.Name, ManagedBy: nj.ManagedBy, CurrentVersion: 1, CreatedAt: n, UpdatedAt: n}
	ver := store.JobVersion{ID: verID, JobID: jobID, Version: 1, Spec: nj.Spec, Checksum: nj.Checksum, CreatedAt: n}
	return job, ver, nil
}

// CreateJobVersion implements store.JobStore.
func (s *Store) CreateJobVersion(ctx context.Context, jobID string, spec json.RawMessage, checksum string,
	expectedCurrent int, now time.Time) (store.JobVersion, error) {
	if err := store.ValidateSpec(spec, checksum); err != nil {
		return store.JobVersion{}, err
	}
	verID, err := store.NewID()
	if err != nil {
		return store.JobVersion{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.JobVersion{}, fmt.Errorf("beginning job version create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	next := expectedCurrent + 1
	n, err := q.SetJobVersionCAS(ctx, gen.SetJobVersionCASParams{
		NewVersion: int64(next), UpdatedAt: fmtTime(now), ID: jobID, ExpectedVersion: int64(expectedCurrent),
	})
	if err != nil {
		return store.JobVersion{}, fmt.Errorf("advancing job %q version: %w", jobID, err)
	}
	if n == 0 {
		if _, err := getJob(ctx, q, jobID); err != nil {
			return store.JobVersion{}, err
		}
		return store.JobVersion{}, fmt.Errorf("job %q not at version %d: %w", jobID, expectedCurrent, store.ErrStale)
	}
	if _, err := q.InsertJobVersion(ctx, gen.InsertJobVersionParams{
		ID: verID, JobID: jobID, Version: int64(next), Spec: string(spec), Checksum: checksum, CreatedAt: fmtTime(now),
	}); err != nil {
		return store.JobVersion{}, fmt.Errorf("creating version %d of job %q: %w", next, jobID, err)
	}
	if err := tx.Commit(); err != nil {
		return store.JobVersion{}, fmt.Errorf("committing job version create: %w", err)
	}
	return store.JobVersion{ID: verID, JobID: jobID, Version: next, Spec: spec, Checksum: checksum, CreatedAt: store.NormalizeTime(now)}, nil
}

// GetJob implements store.JobStore.
func (s *Store) GetJob(ctx context.Context, id string) (store.Job, error) {
	return getJob(ctx, s.q, id)
}

// getJob reads through q, so callers inside a transaction stay on its
// connection (a nested pool acquisition can deadlock a small pool).
func getJob(ctx context.Context, q *gen.Queries, id string) (store.Job, error) {
	row, err := q.GetJob(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Job{}, fmt.Errorf("job %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Job{}, fmt.Errorf("getting job %q: %w", id, err)
	}
	return toJob(row)
}

// GetJobByName implements store.JobStore.
func (s *Store) GetJobByName(ctx context.Context, projectID, name string) (store.Job, error) {
	row, err := s.q.GetJobByName(ctx, gen.GetJobByNameParams{ProjectID: projectID, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return store.Job{}, fmt.Errorf("job %q: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return store.Job{}, fmt.Errorf("getting job %q: %w", name, err)
	}
	return toJob(row)
}

// ListJobs implements store.JobStore.
func (s *Store) ListJobs(ctx context.Context, projectID, cursor string, limit int) (store.JobPage, error) {
	limit = store.ClampLimit(limit)
	rows, err := s.q.ListJobsAfter(ctx, gen.ListJobsAfterParams{ProjectID: projectID, AfterID: cursor, RowLimit: int64(limit) + 1})
	if err != nil {
		return store.JobPage{}, fmt.Errorf("listing jobs: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	page := store.JobPage{Jobs: make([]store.Job, 0, len(rows))}
	for _, r := range rows {
		j, err := toJob(r)
		if err != nil {
			return store.JobPage{}, err
		}
		page.Jobs = append(page.Jobs, j)
	}
	if more {
		page.NextCursor = page.Jobs[len(page.Jobs)-1].ID
	}
	return page, nil
}

// GetJobVersion implements store.JobStore.
func (s *Store) GetJobVersion(ctx context.Context, id string) (store.JobVersion, error) {
	row, err := s.q.GetJobVersion(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.JobVersion{}, fmt.Errorf("job version %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.JobVersion{}, fmt.Errorf("getting job version %q: %w", id, err)
	}
	return toJobVersion(row)
}

// GetJobVersionByNumber implements store.JobStore.
func (s *Store) GetJobVersionByNumber(ctx context.Context, jobID string, version int) (store.JobVersion, error) {
	row, err := s.q.GetJobVersionByNumber(ctx, gen.GetJobVersionByNumberParams{JobID: jobID, Version: int64(version)})
	if errors.Is(err, sql.ErrNoRows) {
		return store.JobVersion{}, fmt.Errorf("job %q version %d: %w", jobID, version, store.ErrNotFound)
	}
	if err != nil {
		return store.JobVersion{}, fmt.Errorf("getting job %q version %d: %w", jobID, version, err)
	}
	return toJobVersion(row)
}

// ListJobVersions implements store.JobStore.
func (s *Store) ListJobVersions(ctx context.Context, jobID string, afterVersion, limit int) ([]store.JobVersion, error) {
	rows, err := s.q.ListJobVersionsAfter(ctx, gen.ListJobVersionsAfterParams{
		JobID: jobID, AfterVersion: int64(afterVersion), RowLimit: int64(store.ClampLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("listing versions of job %q: %w", jobID, err)
	}
	out := make([]store.JobVersion, 0, len(rows))
	for _, r := range rows {
		v, err := toJobVersion(r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
