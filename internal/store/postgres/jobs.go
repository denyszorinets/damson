package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

var _ store.JobStore = (*Store)(nil)

func toProject(r gen.Project) store.Project {
	return store.Project{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt.Time.UTC()}
}

func toJob(r gen.Job) store.Job {
	return store.Job{
		ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, ManagedBy: r.ManagedBy,
		CurrentVersion: int(r.CurrentVersion), CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
}

func toJobVersion(r gen.JobVersion) store.JobVersion {
	return store.JobVersion{
		ID: r.ID, JobID: r.JobID, Version: int(r.Version), Spec: json.RawMessage(r.Spec),
		Checksum: r.Checksum, CreatedAt: r.CreatedAt.Time.UTC(),
	}
}

// GetProjectByName implements store.JobStore.
func (s *Store) GetProjectByName(ctx context.Context, name string) (store.Project, error) {
	row, err := s.q.GetProjectByName(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Project{}, fmt.Errorf("project %q: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return store.Project{}, fmt.Errorf("getting project %q: %w", name, err)
	}
	return toProject(row), nil
}

// ListProjects implements store.JobStore.
func (s *Store) ListProjects(ctx context.Context) ([]store.Project, error) {
	rows, err := s.q.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	out := make([]store.Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, toProject(r))
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return store.Job{}, store.JobVersion{}, fmt.Errorf("beginning job create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	_, err = q.InsertJob(ctx, gen.InsertJobParams{
		ID: jobID, ProjectID: nj.ProjectID, Name: nj.Name, ManagedBy: nj.ManagedBy,
		CurrentVersion: 1, CreatedAt: ts(now), UpdatedAt: ts(now),
	})
	if hasCode(err, uniqueViolation) {
		return store.Job{}, store.JobVersion{}, fmt.Errorf("job %q: %w", nj.Name, store.ErrConflict)
	}
	if hasCode(err, foreignKeyViolation) {
		return store.Job{}, store.JobVersion{}, fmt.Errorf("project %q: %w", nj.ProjectID, store.ErrNotFound)
	}
	if err != nil {
		return store.Job{}, store.JobVersion{}, fmt.Errorf("creating job %q: %w", nj.Name, err)
	}
	if _, err := q.InsertJobVersion(ctx, gen.InsertJobVersionParams{
		ID: verID, JobID: jobID, Version: 1, Spec: string(nj.Spec), Checksum: nj.Checksum, CreatedAt: ts(now),
	}); err != nil {
		return store.Job{}, store.JobVersion{}, fmt.Errorf("creating version 1 of job %q: %w", nj.Name, err)
	}
	if err := tx.Commit(ctx); err != nil {
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return store.JobVersion{}, fmt.Errorf("beginning job version create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	next := expectedCurrent + 1
	n, err := q.SetJobVersionCAS(ctx, gen.SetJobVersionCASParams{
		NewVersion: int32(next), UpdatedAt: ts(now), ID: jobID, ExpectedVersion: int32(expectedCurrent),
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
		ID: verID, JobID: jobID, Version: int32(next), Spec: string(spec), Checksum: checksum, CreatedAt: ts(now),
	}); err != nil {
		return store.JobVersion{}, fmt.Errorf("creating version %d of job %q: %w", next, jobID, err)
	}
	if err := tx.Commit(ctx); err != nil {
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
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Job{}, fmt.Errorf("job %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Job{}, fmt.Errorf("getting job %q: %w", id, err)
	}
	return toJob(row), nil
}

// GetJobByName implements store.JobStore.
func (s *Store) GetJobByName(ctx context.Context, projectID, name string) (store.Job, error) {
	row, err := s.q.GetJobByName(ctx, gen.GetJobByNameParams{ProjectID: projectID, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Job{}, fmt.Errorf("job %q: %w", name, store.ErrNotFound)
	}
	if err != nil {
		return store.Job{}, fmt.Errorf("getting job %q: %w", name, err)
	}
	return toJob(row), nil
}

// ListJobs implements store.JobStore.
func (s *Store) ListJobs(ctx context.Context, projectID, cursor string, limit int) (store.JobPage, error) {
	limit = store.ClampLimit(limit)
	rows, err := s.q.ListJobsAfter(ctx, gen.ListJobsAfterParams{ProjectID: projectID, AfterID: cursor, RowLimit: int32(limit) + 1})
	if err != nil {
		return store.JobPage{}, fmt.Errorf("listing jobs: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	page := store.JobPage{Jobs: make([]store.Job, 0, len(rows))}
	for _, r := range rows {
		page.Jobs = append(page.Jobs, toJob(r))
	}
	if more {
		page.NextCursor = page.Jobs[len(page.Jobs)-1].ID
	}
	return page, nil
}

// GetJobVersion implements store.JobStore.
func (s *Store) GetJobVersion(ctx context.Context, id string) (store.JobVersion, error) {
	row, err := s.q.GetJobVersion(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.JobVersion{}, fmt.Errorf("job version %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.JobVersion{}, fmt.Errorf("getting job version %q: %w", id, err)
	}
	return toJobVersion(row), nil
}

// GetJobVersionByNumber implements store.JobStore.
func (s *Store) GetJobVersionByNumber(ctx context.Context, jobID string, version int) (store.JobVersion, error) {
	row, err := s.q.GetJobVersionByNumber(ctx, gen.GetJobVersionByNumberParams{JobID: jobID, Version: int32(version)})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.JobVersion{}, fmt.Errorf("job %q version %d: %w", jobID, version, store.ErrNotFound)
	}
	if err != nil {
		return store.JobVersion{}, fmt.Errorf("getting job %q version %d: %w", jobID, version, err)
	}
	return toJobVersion(row), nil
}

// ListJobVersions implements store.JobStore.
func (s *Store) ListJobVersions(ctx context.Context, jobID string, afterVersion, limit int) ([]store.JobVersion, error) {
	rows, err := s.q.ListJobVersionsAfter(ctx, gen.ListJobVersionsAfterParams{
		JobID: jobID, AfterVersion: int32(afterVersion), RowLimit: int32(store.ClampLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("listing versions of job %q: %w", jobID, err)
	}
	out := make([]store.JobVersion, 0, len(rows))
	for _, r := range rows {
		out = append(out, toJobVersion(r))
	}
	return out, nil
}
