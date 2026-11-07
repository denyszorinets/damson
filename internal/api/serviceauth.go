package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

// ServiceTokenPrefix marks a service account token. Like the other prefixes it
// keeps a token from being accepted where another kind is expected.
const ServiceTokenPrefix = "dms_"

// Principal is who a request acts as, once its token was checked.
type Principal struct {
	Actor string // admin-token:<hash prefix> or sa:<service account id>
	// Token is set for a service account token.
	Token *store.ServiceToken
}

type principalKey struct{}

// PrincipalFrom returns the checked principal of a request, if it sent a token.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// subject says what a route addresses, so the token's project scope can be
// checked against the project of the target.
type subject int

const (
	subjNone     subject = iota // no project: the token must cover every project
	subjJob                     // path {id} is a job id
	subjRun                     // path {id} is a run id
	subjExec                    // path {id} is an execution id
	subjProject                 // query "project" (default "default")
	subjListRuns                // query "job" if set, else every project
)

type routePolicy struct {
	role string
	on   subject
}

// routePolicies are the operations a service token may use, by operation id.
// Every other operation is unaffected (the admin routes keep requiring the
// admin token; the rest stay as they were).
var routePolicies = map[string]routePolicy{
	"validate-job":          {store.RoleViewer, subjNone},
	"create-job":            {store.RoleEditor, subjNone},
	"list-jobs":             {store.RoleViewer, subjProject},
	"get-job":               {store.RoleViewer, subjJob},
	"create-job-version":    {store.RoleEditor, subjJob},
	"list-job-versions":     {store.RoleViewer, subjJob},
	"get-job-version":       {store.RoleViewer, subjJob},
	"get-job-version-graph": {store.RoleViewer, subjJob},
	"trigger-run":           {store.RoleRunner, subjJob},
	"list-runs":             {store.RoleViewer, subjListRuns},
	"get-run":               {store.RoleViewer, subjRun},
	"get-run-graph":         {store.RoleViewer, subjRun},
	"stream-run-events":     {store.RoleViewer, subjRun},
	"cancel-run":            {store.RoleRunner, subjRun},
	"get-execution":         {store.RoleViewer, subjExec},
	"stream-execution-logs": {store.RoleViewer, subjExec},
}

// covers reports whether a token scoped to scope may act in project: "" covers
// every project, otherwise the project path itself and its descendants.
func covers(scope, project string) bool {
	return scope == "" || project == scope || strings.HasPrefix(project, scope+"/")
}

// authorize is the middleware of the operations in routePolicies (ADR-0060).
//
// A request without Authorization passes unless RequireToken is set (the
// interim state until OIDC, M8). One with a token is always checked: the admin
// token may do everything; a service token needs a role at least the route's
// and a project scope that covers the target. An out-of-scope target answers
// 404, like a missing one.
func authorize(api huma.API, d deps) func(ctx huma.Context, next func(huma.Context)) {
	deny := func(ctx huma.Context, status int, msg string) {
		if status == http.StatusUnauthorized {
			ctx.SetHeader("WWW-Authenticate", `Bearer realm="damson"`)
		}
		_ = huma.WriteErr(api, ctx, status, msg)
	}
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()
		pol, governed := routePolicies[op.OperationID]
		if !governed {
			next(ctx)
			return
		}
		h := ctx.Header("Authorization")
		tok := ""
		if len(h) >= 7 && strings.EqualFold(h[:7], "Bearer ") {
			tok = strings.TrimSpace(h[7:])
			if tok == "" { // a client that means to send a token must not run anonymously
				deny(ctx, http.StatusUnauthorized, "the token is empty")
				return
			}
		} else if strings.HasPrefix(h, ServiceTokenPrefix) || strings.HasPrefix(h, tokens.AdminPrefix) {
			deny(ctx, http.StatusUnauthorized, "send the token as: Authorization: Bearer <token>")
			return
		}
		if tok == "" {
			if d.requireToken {
				deny(ctx, http.StatusUnauthorized, "a token is required")
				return
			}
			next(ctx)
			return
		}
		if d.ds == nil {
			deny(ctx, http.StatusServiceUnavailable, "a required dependency is not available")
			return
		}
		switch {
		case strings.HasPrefix(tok, tokens.AdminPrefix):
			actor, ok := d.adminActor(ctx.Context(), tok)
			if !ok {
				deny(ctx, http.StatusUnauthorized, "the token is not valid")
				return
			}
			next(huma.WithContext(ctx, context.WithValue(ctx.Context(), principalKey{}, Principal{Actor: actor})))
		case strings.HasPrefix(tok, ServiceTokenPrefix):
			st, err := d.ds.FindServiceToken(ctx.Context(), tokens.Hash(tok), d.now())
			if errors.Is(err, store.ErrNotFound) {
				deny(ctx, http.StatusUnauthorized, "the token is not valid")
				return
			}
			if err != nil {
				deny(ctx, http.StatusServiceUnavailable, "a required dependency is not available")
				return
			}
			if store.RoleRank(st.Role) < store.RoleRank(pol.role) {
				deny(ctx, http.StatusForbidden, "the token's role does not allow this")
				return
			}
			if ok, err := d.inScope(ctx, st.Project, pol.on); err != nil {
				deny(ctx, http.StatusServiceUnavailable, "a required dependency is not available")
				return
			} else if !ok {
				denyNotFound(api, ctx)
				return
			}
			_ = d.ds.TouchServiceToken(ctx.Context(), st.ID, d.now()) // best effort
			p := Principal{Actor: "sa:" + st.ServiceAccountID, Token: &st}
			next(huma.WithContext(ctx, context.WithValue(ctx.Context(), principalKey{}, p)))
		default:
			deny(ctx, http.StatusUnauthorized, "the token is not valid")
		}
	}
}

// denyNotFound answers like the handlers do for a missing target, so a target
// out of the token's scope cannot be told from one that does not exist.
func denyNotFound(api huma.API, ctx huma.Context) {
	p := problemFor(store.ErrNotFound)
	_ = huma.WriteErr(api, ctx, p.Status, p.Detail)
}

// adminActor checks an admin token against the stored hash and returns its
// audit actor.
func (d deps) adminActor(ctx context.Context, tok string) (string, bool) {
	s, err := d.ds.GetSetting(ctx, tokens.AdminSettingKey)
	if err != nil || s.Value == "" {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(tokens.Hash(tok)), []byte(s.Value)) != 1 {
		return "", false
	}
	return "admin-token:" + s.Value[:min(12, len(s.Value))], true
}

// inScope reports whether a token scoped to scope may act on what the request
// addresses. A target that does not exist is in scope: the handler answers 404.
func (d deps) inScope(ctx huma.Context, scope string, on subject) (bool, error) {
	if scope == "" {
		return true, nil
	}
	var project string
	var err error
	switch on {
	case subjNone:
		return false, nil // only an unrestricted token
	case subjProject:
		project = ctx.Query("project")
		if project == "" {
			project = store.DefaultProjectName
		}
	case subjListRuns:
		job := ctx.Query("job")
		if job == "" {
			return false, nil
		}
		project, err = d.projectOfJob(ctx.Context(), job)
	case subjJob:
		project, err = d.projectOfJob(ctx.Context(), ctx.Param("id"))
	case subjRun:
		project, err = d.projectOfRun(ctx.Context(), ctx.Param("id"))
	case subjExec:
		var ex store.Execution
		if ex, err = d.ds.GetExecution(ctx.Context(), ctx.Param("id")); err == nil {
			if project, err = d.projectOfRun(ctx.Context(), ex.RunID); errors.Is(err, store.ErrNotFound) {
				err = errNoParent
			}
		}
	}
	if errors.Is(err, errNoParent) {
		return false, nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return covers(scope, project), nil
}

// errNoParent marks a target whose job or project row is missing: the scope
// cannot be decided, so the request is refused.
var errNoParent = errors.New("the target's project is unknown")

func (d deps) projectOfRun(ctx context.Context, runID string) (string, error) {
	run, err := d.ds.GetRun(ctx, runID)
	if err != nil {
		return "", err // the run itself is missing: the handler answers 404
	}
	return d.projectOfJobRow(ctx, run.JobID)
}

func (d deps) projectOfJob(ctx context.Context, jobID string) (string, error) {
	if _, err := d.ds.GetJob(ctx, jobID); err != nil {
		return "", err // the job itself is missing: the handler answers 404
	}
	return d.projectOfJobRow(ctx, jobID)
}

// projectOfJobRow is the project name of a job that a found run or execution
// points at; a missing job or project is errNoParent.
func (d deps) projectOfJobRow(ctx context.Context, jobID string) (string, error) {
	job, err := d.ds.GetJob(ctx, jobID)
	if errors.Is(err, store.ErrNotFound) {
		return "", errNoParent
	}
	if err != nil {
		return "", err
	}
	ps, err := d.ds.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range ps {
		if p.ID == job.ProjectID {
			return p.Name, nil
		}
	}
	return "", errNoParent
}
