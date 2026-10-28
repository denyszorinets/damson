package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
)

// Execution is one attempt of a step instance. The attempt id (the fencing
// token) is deliberately not part of the API.
type Execution struct {
	ID          string          `json:"id"`
	Attempt     int             `json:"attempt" doc:"Attempt number, from 1."`
	State       string          `json:"state" enum:"queued,dispatched,running,succeeded,failed,canceled,timed_out,lost"`
	Pool        string          `json:"pool"`
	AgentID     string          `json:"agent_id,omitempty"`
	ScheduledAt time.Time       `json:"scheduled_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	Outputs     json.RawMessage `json:"outputs,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// Step is a step instance with its executions, oldest attempt first.
type Step struct {
	ID         string          `json:"id"`
	Path       string          `json:"path"`
	Kind       string          `json:"kind" enum:"step,group"`
	Plugin     string          `json:"plugin,omitempty"`
	Input      json.RawMessage `json:"input,omitempty" doc:"Resolved plugin input; holds secret:// references, never secret values."`
	Status     string          `json:"status" enum:"pending,running,succeeded,failed,skipped,canceled"`
	Outputs    json.RawMessage `json:"outputs,omitempty"`
	Error      string          `json:"error,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Executions []Execution     `json:"executions"`
}

// Run is the API view of a run without its step tree.
type Run struct {
	ID                string          `json:"id"`
	JobID             string          `json:"job_id"`
	JobVersionID      string          `json:"job_version_id"`
	Status            string          `json:"status" enum:"pending,running,succeeded,failed,canceled"`
	Trigger           json.RawMessage `json:"trigger,omitempty"`
	Options           json.RawMessage `json:"options,omitempty" doc:"Resolved options with defaults applied; secrets appear as secret:// references only."`
	CancelRequestedAt *time.Time      `json:"cancel_requested_at,omitempty"`
	ConcurrencyKey    string          `json:"concurrency_key,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	StartedAt         *time.Time      `json:"started_at,omitempty"`
	FinishedAt        *time.Time      `json:"finished_at,omitempty"`
}

// RunDetail is a run with its step tree.
type RunDetail struct {
	Run
	JobVersion int    `json:"job_version" doc:"Number of the pinned job version."`
	Steps      []Step `json:"steps" doc:"Step instances in creation order."`
}

func rawJSON(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	return b
}

func toRun(r store.Run) Run {
	return Run{
		ID: r.ID, JobID: r.JobID, JobVersionID: r.JobVersionID, Status: string(r.Status),
		Trigger: rawJSON(r.Trigger), Options: rawJSON(r.Options), CancelRequestedAt: r.CancelRequestedAt,
		ConcurrencyKey: r.ConcurrencyKey, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
}

func toExecution(e store.Execution) Execution {
	return Execution{
		ID: e.ID, Attempt: e.AttemptNo, State: string(e.State), Pool: e.Pool, AgentID: e.AgentID,
		ScheduledAt: e.ScheduledAt, StartedAt: e.StartedAt, FinishedAt: e.FinishedAt,
		Outputs: rawJSON(e.Outputs), Error: e.Error,
	}
}

func toStep(n store.StepNode) Step {
	s := Step{
		ID: n.ID, Path: n.Path, Kind: n.Kind, Plugin: n.Plugin, Input: rawJSON(n.Input), Status: string(n.Status),
		Outputs: rawJSON(n.Outputs), Error: n.Error, CreatedAt: n.CreatedAt, StartedAt: n.StartedAt, FinishedAt: n.FinishedAt,
		Executions: make([]Execution, 0, len(n.Executions)),
	}
	for _, e := range n.Executions {
		s.Executions = append(s.Executions, toExecution(e))
	}
	return s
}

func toRunDetail(t store.RunTree, jobVersion int) RunDetail {
	d := RunDetail{Run: toRun(t.Run), JobVersion: jobVersion, Steps: make([]Step, 0, len(t.Steps))}
	for _, n := range t.Steps {
		d.Steps = append(d.Steps, toStep(n))
	}
	return d
}

// loadRunDetail reads a run tree and the number of its pinned version.
func loadRunDetail(ctx context.Context, ds DataStore, id string) (RunDetail, error) {
	t, err := ds.GetRunTree(ctx, id)
	if err != nil {
		return RunDetail{}, err
	}
	v, err := ds.GetJobVersion(ctx, t.JobVersionID)
	if err != nil {
		return RunDetail{}, err
	}
	return toRunDetail(t, v.Version), nil
}

type triggerBody struct {
	Options           map[string]any `json:"options,omitempty" doc:"Run options; validated against spec.options, defaults applied."`
	Version           *int           `json:"version,omitempty" minimum:"1" doc:"Job version to run; defaults to the current version."`
	ConcurrencyPolicy string         `json:"concurrency_policy,omitempty" enum:"queue,skip,cancel_previous" doc:"Only queue (the default) is supported until M3; the others answer 422."`
}

// triggerRunInput has an optional body (a pointer): triggering with no
// options is a plain POST.
type triggerRunInput struct {
	ID   string `path:"id" maxLength:"128"`
	Body *triggerBody
}

type runDetailOutput struct{ Body RunDetail }

type runIDInput struct {
	ID string `path:"id" maxLength:"128"`
}

type listRunsInput struct {
	Cursor string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor."`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
	Job    string `query:"job" maxLength:"128" doc:"Only runs of this job id."`
	Status string `query:"status" enum:"pending,running,succeeded,failed,canceled" doc:"Only runs in this status."`
}

type listRunsOutput struct {
	Body struct {
		Items      []Run  `json:"items" doc:"Newest first."`
		NextCursor string `json:"next_cursor,omitempty" doc:"Empty on the last page."`
	}
}

type cancelRunOutput struct{ Body Run }

func registerRuns(api huma.API, d deps) {
	huma.Register(api, huma.Operation{
		OperationID: "trigger-run", Method: http.MethodPost, Path: "/jobs/{id}/runs",
		Summary: "Trigger a run through the API", Tags: []string{"runs"}, DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *triggerRunInput) (*runDetailOutput, error) {
		return triggerRun(ctx, d, in)
	})
	huma.Register(api, huma.Operation{
		OperationID: "list-runs", Method: http.MethodGet, Path: "/runs",
		Summary: "List runs, newest first", Tags: []string{"runs"},
	}, func(ctx context.Context, in *listRunsInput) (*listRunsOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		cursor, err := decodeCursor(ctx, in.Cursor)
		if err != nil {
			return nil, err
		}
		page, err := d.ds.QueryRuns(ctx, store.RunFilter{JobID: in.Job, Status: store.RunStatus(in.Status)}, cursor, in.Limit)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		out := &listRunsOutput{}
		out.Body.Items = make([]Run, 0, len(page.Runs))
		for _, r := range page.Runs {
			out.Body.Items = append(out.Body.Items, toRun(r))
		}
		out.Body.NextCursor = encodeCursor(page.NextCursor)
		return out, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "get-run", Method: http.MethodGet, Path: "/runs/{id}",
		Summary: "Get a run with its step tree", Tags: []string{"runs"}, Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, in *runIDInput) (*runDetailOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		det, err := loadRunDetail(ctx, d.ds, in.ID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		return &runDetailOutput{Body: det}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "cancel-run", Method: http.MethodPost, Path: "/runs/{id}/cancel",
		Summary: "Request cancellation of a run", Tags: []string{"runs"},
		Description: "Records the request; the run is cancelled by the next advance. Idempotent: repeating it while the run is still active answers 200 with the run. A run that already finished answers 409.",
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
	}, func(ctx context.Context, in *runIDInput) (*cancelRunOutput, error) {
		return cancelRun(ctx, d, in.ID)
	})
}

func cancelRun(ctx context.Context, d deps, id string) (*cancelRunOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	run, err := d.ds.RequestCancel(ctx, id, d.now())
	if errors.Is(err, store.ErrStale) {
		// Either the request was already recorded or the run finished. The
		// read below is a separate statement, so the run may change between
		// the two; the answer is still correct for the instant it reads (a
		// run that finishes in that window reports 409, which is true by then).
		cur, gerr := d.ds.GetRun(ctx, id)
		switch {
		case gerr != nil:
			return nil, apiError(ctx, gerr)
		case cur.Status.Terminal():
			return nil, apiError(ctx, errRunFinished)
		}
		run, err = cur, nil
	}
	if err != nil {
		return nil, apiError(ctx, err)
	}
	return &cancelRunOutput{Body: toRun(run)}, nil
}
