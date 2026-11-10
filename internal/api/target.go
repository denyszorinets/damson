package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
)

type targetKind int

const (
	tgtFound    targetKind = iota // project (and maybe job) are known
	tgtMissing                    // the addressed thing does not exist: the handler answers 404
	tgtNoParent                   // it exists but its job or project row is gone: refuse
	tgtNone                       // the route names no project
	tgtAll                        // the route spans every project (list-runs without a job)
)

// target is what a request addresses.
type target struct {
	kind    targetKind
	project string // project path
	jobID   string // set when the target is a job or belongs to one
}

// inTokenScope reports whether a token scoped to scope may act on the target.
// A target that does not exist is in scope, so the handler answers 404; one
// that spans projects, names none or has lost its parent is not.
func (t target) inTokenScope(scope string) bool {
	if scope == "" {
		return true
	}
	switch t.kind {
	case tgtMissing:
		return true
	case tgtFound:
		return covers(scope, t.project)
	}
	return false
}

// target resolves what the request addresses from its path and query.
func (d deps) target(ctx huma.Context, on subject) (target, error) {
	var (
		project, jobID string
		err            error
	)
	switch on {
	case subjNone, subjHandler:
		return target{kind: tgtNone}, nil
	case subjProject:
		project = ctx.Query("project")
		if project == "" {
			project = store.DefaultProjectPath
		}
		if store.ValidateProjectPath(project) != nil {
			return target{kind: tgtMissing}, nil // junk is no project: the handler answers 404
		}
	case subjProjectPath:
		project = ctx.Param("path")
		if store.ValidateProjectPath(project) != nil {
			return target{kind: tgtMissing}, nil
		}
		if _, perr := d.ds.GetProjectByPath(ctx.Context(), project); errors.Is(perr, store.ErrNotFound) {
			return target{kind: tgtMissing}, nil
		} else if perr != nil {
			return target{}, perr
		}
	case subjListRuns:
		if jobID = ctx.Query("job"); jobID == "" {
			return target{kind: tgtAll}, nil
		}
		project, err = d.projectOfJob(ctx.Context(), jobID)
	case subjJob:
		jobID = ctx.Param("id")
		project, err = d.projectOfJob(ctx.Context(), jobID)
	case subjSchedule:
		var sc store.Schedule
		if sc, err = d.ds.GetSchedule(ctx.Context(), ctx.Param("id")); err == nil {
			jobID = sc.JobID
			project, err = d.projectOfJobRow(ctx.Context(), jobID)
		}
	case subjWebhook:
		var w store.Webhook
		if w, err = d.ds.GetWebhook(ctx.Context(), ctx.Param("id")); err == nil {
			jobID = w.JobID
			project, err = d.projectOfJobRow(ctx.Context(), jobID)
		}
	case subjTriggerList:
		switch {
		case ctx.Query("job_id") != "":
			jobID = ctx.Query("job_id")
			project, err = d.projectOfJob(ctx.Context(), jobID)
		case ctx.Query("project") != "":
			project = ctx.Query("project")
			if store.ValidateProjectPath(project) != nil {
				return target{kind: tgtMissing}, nil
			}
			if _, perr := d.ds.GetProjectByPath(ctx.Context(), project); errors.Is(perr, store.ErrNotFound) {
				return target{kind: tgtMissing}, nil
			} else if perr != nil {
				return target{}, perr
			}
		default:
			return target{kind: tgtAll}, nil
		}
	case subjRun:
		project, jobID, err = d.projectOfRun(ctx.Context(), ctx.Param("id"))
	case subjExec:
		var ex store.Execution
		if ex, err = d.ds.GetExecution(ctx.Context(), ctx.Param("id")); err == nil {
			if project, jobID, err = d.projectOfRun(ctx.Context(), ex.RunID); errors.Is(err, store.ErrNotFound) {
				err = errNoParent
			}
		}
	}
	switch {
	case errors.Is(err, errNoParent):
		return target{kind: tgtNoParent}, nil
	case errors.Is(err, store.ErrNotFound):
		return target{kind: tgtMissing}, nil
	case err != nil:
		return target{}, err
	}
	return target{kind: tgtFound, project: project, jobID: jobID}, nil
}

// errNoParent marks a target whose job or project row is missing: the decision
// cannot be made, so the request is refused.
var errNoParent = errors.New("the target's project is unknown")

func (d deps) projectOfRun(ctx context.Context, runID string) (project, jobID string, err error) {
	run, err := d.ds.GetRun(ctx, runID)
	if err != nil {
		return "", "", err // the run itself is missing: the handler answers 404
	}
	project, err = d.projectOfJobRow(ctx, run.JobID)
	return project, run.JobID, err
}

func (d deps) projectOfJob(ctx context.Context, jobID string) (string, error) {
	if _, err := d.ds.GetJob(ctx, jobID); err != nil {
		return "", err // the job itself is missing: the handler answers 404
	}
	return d.projectOfJobRow(ctx, jobID)
}

// projectOfJobRow is the project path of a job that a found run or execution
// points at; a missing job or project is errNoParent.
func (d deps) projectOfJobRow(ctx context.Context, jobID string) (string, error) {
	job, err := d.ds.GetJob(ctx, jobID)
	if errors.Is(err, store.ErrNotFound) {
		return "", errNoParent
	}
	if err != nil {
		return "", err
	}
	p, err := d.ds.GetProject(ctx, job.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		return "", errNoParent
	}
	if err != nil {
		return "", err
	}
	return p.Path, nil
}
