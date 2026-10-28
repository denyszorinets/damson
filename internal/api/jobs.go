package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/store"
)

// Job is the API view of a job.
type Job struct {
	ID             string    `json:"id"`
	Project        string    `json:"project" doc:"Project name."`
	Name           string    `json:"name"`
	ManagedBy      string    `json:"managed_by"`
	CurrentVersion int       `json:"current_version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// JobVersion is one immutable version of a job spec. Spec is present on
// single-version reads and omitted from lists.
type JobVersion struct {
	ID        string          `json:"id"`
	JobID     string          `json:"job_id"`
	Version   int             `json:"version"`
	Checksum  string          `json:"checksum" doc:"SHA-256 of the canonical spec."`
	CreatedAt time.Time       `json:"created_at"`
	Spec      json.RawMessage `json:"spec,omitempty" doc:"The spec document exactly as submitted."`
}

// JobDetail is a job with one of its versions.
type JobDetail struct {
	Job      Job         `json:"job"`
	Version  JobVersion  `json:"version" doc:"The current version on reads, the new version on writes."`
	Warnings []SpecIssue `json:"warnings,omitempty" doc:"Non-fatal findings, such as plugins no agent has reported yet."`
}

func toJobVersion(v store.JobVersion, withSpec bool) JobVersion {
	out := JobVersion{ID: v.ID, JobID: v.JobID, Version: v.Version, Checksum: v.Checksum, CreatedAt: v.CreatedAt}
	if withSpec {
		out.Spec = v.Spec
	}
	return out
}

// projectNames maps project ids to names. M2 has a single project, so the
// whole table is read.
func projectNames(ctx context.Context, ds DataStore) (map[string]string, error) {
	ps, err := ds.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	m := make(map[string]string, len(ps))
	for _, p := range ps {
		m[p.ID] = p.Name
	}
	return m, nil
}

func toJob(j store.Job, projects map[string]string) Job {
	return Job{
		ID: j.ID, Project: projects[j.ProjectID], Name: j.Name, ManagedBy: j.ManagedBy,
		CurrentVersion: j.CurrentVersion, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
	}
}

type createJobInput struct {
	Body struct {
		Name string          `json:"name,omitempty" maxLength:"255" doc:"Job name; defaults to spec.metadata.name and must equal it when given."`
		Spec json.RawMessage `json:"spec" required:"true" doc:"The job document (apiVersion damson.dev/v1, kind Job)."`
	}
}

type jobDetailOutput struct {
	Body JobDetail
}

type listJobsInput struct {
	Cursor  string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor."`
	Limit   int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
	Project string `query:"project" default:"default" doc:"Project name."`
}

type listJobsOutput struct {
	Body struct {
		Items      []Job  `json:"items"`
		NextCursor string `json:"next_cursor,omitempty" doc:"Empty on the last page."`
	}
}

type jobIDInput struct {
	ID string `path:"id" maxLength:"128"`
}

type createVersionInput struct {
	ID   string `path:"id" maxLength:"128"`
	Body struct {
		Spec            json.RawMessage `json:"spec" required:"true"`
		ExpectedVersion *int            `json:"expected_version,omitempty" minimum:"1" doc:"The current_version the caller based this spec on; the write fails with 409 if another version landed meanwhile. Omitted means the version current when the request is handled."`
	}
}

type listVersionsInput struct {
	ID     string `path:"id" maxLength:"128"`
	Cursor string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor."`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
}

type listVersionsOutput struct {
	Body struct {
		Items      []JobVersion `json:"items" doc:"Ascending by version, without the spec."`
		NextCursor string       `json:"next_cursor,omitempty"`
	}
}

type versionInput struct {
	ID      string `path:"id" maxLength:"128"`
	Version int    `path:"v" minimum:"1"`
}

type getVersionOutput struct{ Body JobVersion }

type validateInput struct {
	Body struct {
		Spec json.RawMessage `json:"spec" required:"true"`
	}
}

type validateOutput struct {
	Body struct {
		Valid    bool        `json:"valid"`
		Errors   []SpecIssue `json:"errors"`
		Warnings []SpecIssue `json:"warnings"`
	}
}

func registerJobs(api huma.API, d deps) {
	huma.Register(api, huma.Operation{
		OperationID: "validate-job", Method: http.MethodPost, Path: "/jobs/validate",
		Summary: "Dry-run validation of a job spec", Tags: []string{"jobs"},
		Description: "Always answers 200; invalid specs are reported in errors, with spec paths. Plugins no agent has reported are warnings. The job graph is added in M3.",
	}, func(ctx context.Context, in *validateInput) (*validateOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		c := d.checkSpec(in.Body.Spec)
		out := &validateOutput{}
		out.Body.Valid = len(c.errors) == 0
		out.Body.Errors = nonNilIssues(c.errors)
		out.Body.Warnings = nonNilIssues(c.warnings)
		return out, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "create-job", Method: http.MethodPost, Path: "/jobs",
		Summary: "Create a job with version 1", Tags: []string{"jobs"}, DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *createJobInput) (*jobDetailOutput, error) {
		return createJob(ctx, d, in)
	})
	huma.Register(api, huma.Operation{
		OperationID: "list-jobs", Method: http.MethodGet, Path: "/jobs",
		Summary: "List jobs of a project", Tags: []string{"jobs"}, Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, in *listJobsInput) (*listJobsOutput, error) {
		return listJobs(ctx, d, in)
	})
	huma.Register(api, huma.Operation{
		OperationID: "get-job", Method: http.MethodGet, Path: "/jobs/{id}",
		Summary: "Get a job with its current version and spec", Tags: []string{"jobs"}, Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, in *jobIDInput) (*jobDetailOutput, error) {
		return getJob(ctx, d, in.ID)
	})
	huma.Register(api, huma.Operation{
		OperationID: "create-job-version", Method: http.MethodPost, Path: "/jobs/{id}/versions",
		Summary: "Append an immutable version", Tags: []string{"jobs"}, DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *createVersionInput) (*jobDetailOutput, error) {
		return createVersion(ctx, d, in)
	})
	huma.Register(api, huma.Operation{
		OperationID: "list-job-versions", Method: http.MethodGet, Path: "/jobs/{id}/versions",
		Summary: "List versions of a job", Tags: []string{"jobs"}, Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, in *listVersionsInput) (*listVersionsOutput, error) {
		return listVersions(ctx, d, in)
	})
	huma.Register(api, huma.Operation{
		OperationID: "get-job-version", Method: http.MethodGet, Path: "/jobs/{id}/versions/{v}",
		Summary: "Get one version with its spec", Tags: []string{"jobs"}, Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, in *versionInput) (*getVersionOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		v, err := d.ds.GetJobVersionByNumber(ctx, in.ID, in.Version)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		return &getVersionOutput{Body: toJobVersion(v, true)}, nil
	})
}

func nonNilIssues(s []SpecIssue) []SpecIssue {
	if s == nil {
		return []SpecIssue{}
	}
	return s
}

func createJob(ctx context.Context, d deps, in *createJobInput) (*jobDetailOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	c := d.checkSpec(in.Body.Spec)
	if len(c.errors) > 0 {
		return nil, apiError(ctx, invalid("the job spec is invalid", problemDetails(c.errors)...))
	}
	name := in.Body.Name
	if name == "" {
		name = c.job.Metadata.Name
	}
	if name != c.job.Metadata.Name {
		return nil, apiError(ctx, invalid("name must equal spec.metadata.name",
			ProblemDetail{Message: "differs from spec.metadata.name", Location: "name"}))
	}
	proj, err := d.ds.GetProjectByName(ctx, c.job.Metadata.Project)
	if errors.Is(err, store.ErrNotFound) {
		return nil, apiError(ctx, invalid("unknown project",
			ProblemDetail{Message: "no such project", Location: "spec.metadata.project"}))
	}
	if err != nil {
		return nil, apiError(ctx, err)
	}
	sum, err := engine.Checksum(in.Body.Spec)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	job, ver, err := d.ds.CreateJob(ctx, store.NewJob{ProjectID: proj.ID, Name: name, Spec: in.Body.Spec, Checksum: sum}, d.now())
	if err != nil {
		return nil, apiError(ctx, err)
	}
	return &jobDetailOutput{Body: JobDetail{
		Job:      toJob(job, map[string]string{proj.ID: proj.Name}),
		Version:  toJobVersion(ver, true),
		Warnings: c.warnings,
	}}, nil
}

func getJob(ctx context.Context, d deps, id string) (*jobDetailOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	job, err := d.ds.GetJob(ctx, id)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	ver, err := d.ds.GetJobVersionByNumber(ctx, job.ID, job.CurrentVersion)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	names, err := projectNames(ctx, d.ds)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	return &jobDetailOutput{Body: JobDetail{Job: toJob(job, names), Version: toJobVersion(ver, true)}}, nil
}

func listJobs(ctx context.Context, d deps, in *listJobsInput) (*listJobsOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	cursor, err := decodeCursor(ctx, in.Cursor)
	if err != nil {
		return nil, err
	}
	proj, err := d.ds.GetProjectByName(ctx, in.Project)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	page, err := d.ds.ListJobs(ctx, proj.ID, cursor, in.Limit)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	names := map[string]string{proj.ID: proj.Name}
	out := &listJobsOutput{}
	out.Body.Items = make([]Job, 0, len(page.Jobs))
	for _, j := range page.Jobs {
		out.Body.Items = append(out.Body.Items, toJob(j, names))
	}
	out.Body.NextCursor = encodeCursor(page.NextCursor)
	return out, nil
}

func createVersion(ctx context.Context, d deps, in *createVersionInput) (*jobDetailOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	job, err := d.ds.GetJob(ctx, in.ID)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	c := d.checkSpec(in.Body.Spec)
	if len(c.errors) > 0 {
		return nil, apiError(ctx, invalid("the job spec is invalid", problemDetails(c.errors)...))
	}
	names, err := projectNames(ctx, d.ds)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	if c.job.Metadata.Name != job.Name || c.job.Metadata.Project != names[job.ProjectID] {
		return nil, apiError(ctx, invalid("a new version cannot change the job's project or name",
			ProblemDetail{Message: "must equal " + strconv.Quote(job.Name) + " in project " + strconv.Quote(names[job.ProjectID]), Location: "spec.metadata"}))
	}
	sum, err := engine.Checksum(in.Body.Spec)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	expected := job.CurrentVersion
	if in.Body.ExpectedVersion != nil {
		expected = *in.Body.ExpectedVersion
	}
	ver, err := d.ds.CreateJobVersion(ctx, job.ID, in.Body.Spec, sum, expected, d.now())
	if err != nil {
		return nil, apiError(ctx, err)
	}
	job, err = d.ds.GetJob(ctx, job.ID)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	return &jobDetailOutput{Body: JobDetail{Job: toJob(job, names), Version: toJobVersion(ver, true), Warnings: c.warnings}}, nil
}

func listVersions(ctx context.Context, d deps, in *listVersionsInput) (*listVersionsOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	raw, err := decodeCursor(ctx, in.Cursor)
	if err != nil {
		return nil, err
	}
	after := 0
	if raw != "" {
		if after, err = strconv.Atoi(raw); err != nil || after < 0 {
			return nil, apiError(ctx, invalid("invalid cursor", ProblemDetail{Message: "invalid cursor", Location: "query.cursor"}))
		}
	}
	if _, err := d.ds.GetJob(ctx, in.ID); err != nil {
		return nil, apiError(ctx, err)
	}
	vs, err := d.ds.ListJobVersions(ctx, in.ID, after, in.Limit+1)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	out := &listVersionsOutput{}
	if len(vs) > in.Limit {
		vs = vs[:in.Limit]
		out.Body.NextCursor = encodeCursor(strconv.Itoa(vs[len(vs)-1].Version))
	}
	out.Body.Items = make([]JobVersion, 0, len(vs))
	for _, v := range vs {
		out.Body.Items = append(out.Body.Items, toJobVersion(v, false))
	}
	return out, nil
}
