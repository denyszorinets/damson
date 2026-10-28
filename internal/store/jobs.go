package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// DefaultProjectID and DefaultProjectName identify the project seeded by
// migration 00003. M2 has exactly one project; the tree comes later.
const (
	DefaultProjectID   = "00000000-0000-7000-8000-000000000001"
	DefaultProjectName = "default"
)

// Project groups jobs.
type Project struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// Job is a named job pointing at its current version number.
type Job struct {
	ID             string
	ProjectID      string
	Name           string
	ManagedBy      string // "ui", "files:<source>" or "git:<source>"
	CurrentVersion int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// JobVersion is an immutable job spec. Spec is returned byte for byte as
// stored, so Checksum can be re-verified by the caller.
type JobVersion struct {
	ID        string
	JobID     string
	Version   int
	Spec      json.RawMessage
	Checksum  string
	CreatedAt time.Time
}

// NewJob creates a job together with its version 1.
type NewJob struct {
	ProjectID string
	Name      string
	ManagedBy string // defaults to "ui"
	Spec      json.RawMessage
	Checksum  string
}

// JobPage is one keyset page of jobs; NextCursor is empty on the last page.
type JobPage struct {
	Jobs       []Job
	NextCursor string
}

// JobStore persists projects, jobs and their immutable versions.
type JobStore interface {
	// GetProjectByName returns ErrNotFound if absent.
	GetProjectByName(ctx context.Context, name string) (Project, error)
	ListProjects(ctx context.Context) ([]Project, error)

	// CreateJob inserts the job and its version 1 in one transaction. It
	// returns ErrNotFound if the project is absent, ErrConflict if the name
	// is taken in the project, and ErrInvalid for an empty name or checksum,
	// or a spec that is not valid JSON.
	CreateJob(ctx context.Context, nj NewJob, now time.Time) (Job, JobVersion, error)
	// CreateJobVersion appends the next version and moves the job's
	// current_version, as a CAS on expectedCurrent. It returns ErrNotFound if
	// the job is absent and ErrStale if expectedCurrent is not current.
	CreateJobVersion(ctx context.Context, jobID string, spec json.RawMessage, checksum string,
		expectedCurrent int, now time.Time) (JobVersion, error)
	// GetJob and GetJobByName return ErrNotFound if absent.
	GetJob(ctx context.Context, id string) (Job, error)
	GetJobByName(ctx context.Context, projectID, name string) (Job, error)
	// ListJobs is keyset-paginated by id within a project.
	ListJobs(ctx context.Context, projectID, cursor string, limit int) (JobPage, error)
	// GetJobVersion returns ErrNotFound if absent.
	GetJobVersion(ctx context.Context, id string) (JobVersion, error)
	GetJobVersionByNumber(ctx context.Context, jobID string, version int) (JobVersion, error)
	// ListJobVersions returns versions greater than afterVersion, ascending.
	ListJobVersions(ctx context.Context, jobID string, afterVersion, limit int) ([]JobVersion, error)
}

// ValidateNewJob rejects jobs the store cannot persist, before any I/O.
func ValidateNewJob(nj NewJob) error {
	if nj.ProjectID == "" || nj.Name == "" {
		return fmt.Errorf("job needs a project and a name: %w", ErrInvalid)
	}
	return ValidateSpec(nj.Spec, nj.Checksum)
}

// ValidateSpec rejects an empty checksum or a spec that is not valid JSON.
func ValidateSpec(spec json.RawMessage, checksum string) error {
	if checksum == "" {
		return fmt.Errorf("job version needs a checksum: %w", ErrInvalid)
	}
	if len(spec) == 0 {
		return fmt.Errorf("job spec is empty: %w", ErrInvalid)
	}
	return checkJSON("job spec", spec)
}
