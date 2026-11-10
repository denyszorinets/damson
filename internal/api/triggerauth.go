package api

import (
	"context"
	"net/http"

	"github.com/denyszorinets/damson/internal/store"
)

// requireOnJob is the check of a route whose body names the job (create a
// schedule or a webhook): the caller needs at least need on the job's project,
// counting the job's own rules. A job that does not exist, and one the caller
// holds no role on, are both store.ErrNotFound (ADR-0076).
func (d deps) requireOnJob(ctx context.Context, jobID, need string) error {
	project, err := d.projectOfJob(ctx, jobID)
	if err != nil {
		return err
	}
	return d.require(ctx, project, jobID, need)
}

// checkTriggerList guards the list of schedules or webhooks: it answers 404 for
// a job or project that is missing or that the caller holds no role on (the
// middleware leaves a missing target to the handler), refuses a request that
// names both, and lets only an instance admin or a token covering every project
// list without naming either.
func (d deps) checkTriggerList(ctx context.Context, jobID, project string) error {
	bad := func(msg string) error {
		p := newHumaError(http.StatusBadRequest, msg).(*Problem)
		p.RequestID = requestIDFromCtx(ctx)
		return p
	}
	if jobID != "" && project != "" {
		return bad("give job_id or project, not both")
	}
	if p, ok := PrincipalFrom(ctx); ok && p.InstanceAdmin && (jobID != "" || project != "") {
		return nil // an instance admin sees everything, so a missing job or project is an empty list as before
	}
	switch {
	case jobID != "": // a missing job and one the caller holds no role on both answer 404
		if err := d.requireOnJob(ctx, jobID, store.RoleViewer); err != nil {
			return apiError(ctx, err)
		}
		return nil
	case project != "":
		if _, err := d.ds.GetProjectByPath(ctx, project); err != nil {
			return apiError(ctx, err)
		}
		if err := d.require(ctx, project, "", store.RoleViewer); err != nil {
			return apiError(ctx, err)
		}
		return nil
	}
	if p, ok := PrincipalFrom(ctx); ok && (p.InstanceAdmin || p.Token != nil) {
		return nil // a token reaches here only when its scope covers every project
	}
	return bad("job_id or project is required")
}
