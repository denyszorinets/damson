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

// Principal is who a request acts as, once its credentials were checked.
type Principal struct {
	Actor string // admin-token:<hash prefix>, sa:<service account id> or user:<sub>
	// Token is set for a service account token.
	Token *store.ServiceToken
	// Session is set for a logged-in user; CSRF is the token its writes carry.
	Session *store.Session
	CSRF    string
	// InstanceAdmin is true for the admin token and for a user the admin rule
	// selected at login: they may do everything.
	InstanceAdmin bool
}

type principalKey struct{}

// PrincipalFrom returns the checked principal of a request, if it was authenticated.
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

// openOps need no login even when OIDC is on: static data and health.
var openOps = map[string]bool{"list-secret-types": true}

// authorize is the global middleware of the API (ADR-0060, ADR-0063).
//
// The operations in routePolicies are governed: an admin (the admin token or an
// instance-admin user) may do everything; a service token needs a role at
// least the route's and a project scope that covers the target (out of scope
// answers 404, like a missing target); a user who is no instance admin is
// refused until access rules exist. Without credentials a request passes only
// when OIDC is off and RequireToken is not set. With OIDC on, every operation
// that is neither open nor guarded by requireAdmin needs some principal.
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
		needsLogin := d.oidc != nil && !governed && len(op.Security) == 0 && !openOps[op.OperationID]
		if !governed && !needsLogin && op.OperationID != meOperation {
			next(ctx)
			return
		}
		p, status, msg := d.authenticate(ctx)
		if status != 0 {
			deny(ctx, status, msg)
			return
		}
		if p.Actor == "" { // anonymous: allowed only where nothing demands a login
			next(ctx)
			return
		}
		rctx := huma.WithContext(ctx, context.WithValue(ctx.Context(), principalKey{}, p))
		switch {
		case !governed || p.InstanceAdmin:
			next(rctx)
		case p.Token != nil:
			if store.RoleRank(p.Token.Role) < store.RoleRank(pol.role) {
				deny(ctx, http.StatusForbidden, "the token's role does not allow this")
				return
			}
			if ok, err := d.inScope(ctx, p.Token.Project, pol.on); err != nil {
				deny(ctx, http.StatusServiceUnavailable, "a required dependency is not available")
				return
			} else if !ok {
				denyNotFound(api, ctx)
				return
			}
			_ = d.ds.TouchServiceToken(ctx.Context(), p.Token.ID, d.now()) // best effort
			next(rctx)
		default: // a user without the admin role: access rules arrive with the next slice
			deny(ctx, http.StatusForbidden, "your account has no access to this")
		}
	}
}

const meOperation = "get-me"

// authenticate checks the credentials of a request: a Bearer token (the admin
// token or a service token), else with OIDC on the session cookie. A token that
// is sent always wins over a cookie. A zero principal and status 0 mean
// anonymous; otherwise status and msg are the refusal.
func (d deps) authenticate(ctx huma.Context) (p Principal, status int, msg string) {
	h := ctx.Header("Authorization")
	tok := ""
	if len(h) >= 7 && strings.EqualFold(h[:7], "Bearer ") {
		tok = strings.TrimSpace(h[7:])
		if tok == "" { // a client that means to send a token must not run anonymously
			return p, http.StatusUnauthorized, "the token is empty"
		}
	} else if strings.HasPrefix(h, ServiceTokenPrefix) || strings.HasPrefix(h, tokens.AdminPrefix) {
		return p, http.StatusUnauthorized, "send the token as: Authorization: Bearer <token>"
	}
	if tok == "" {
		return d.authenticateCookie(ctx)
	}
	if d.ds == nil {
		return p, http.StatusServiceUnavailable, "a required dependency is not available"
	}
	switch {
	case strings.HasPrefix(tok, tokens.AdminPrefix):
		actor, ok := d.adminActor(ctx.Context(), tok)
		if !ok {
			return p, http.StatusUnauthorized, "the token is not valid"
		}
		return Principal{Actor: actor, InstanceAdmin: true}, 0, ""
	case strings.HasPrefix(tok, ServiceTokenPrefix):
		st, err := d.ds.FindServiceToken(ctx.Context(), tokens.Hash(tok), d.now())
		if errors.Is(err, store.ErrNotFound) {
			return p, http.StatusUnauthorized, "the token is not valid"
		}
		if err != nil {
			return p, http.StatusServiceUnavailable, "a required dependency is not available"
		}
		return Principal{Actor: "sa:" + st.ServiceAccountID, Token: &st}, 0, ""
	}
	return p, http.StatusUnauthorized, "the token is not valid"
}

// authenticateCookie is the no-token case: a session with OIDC on, otherwise
// anonymous unless RequireToken is set.
func (d deps) authenticateCookie(ctx huma.Context) (p Principal, status int, msg string) {
	if d.oidc == nil {
		if d.requireToken {
			return p, http.StatusUnauthorized, "a token is required"
		}
		return p, 0, ""
	}
	if d.ds == nil {
		return p, http.StatusServiceUnavailable, "a required dependency is not available"
	}
	s, tok, err := d.session(ctx.Context(), sessionRequest{
		cookies: ctx.Header("Cookie"), method: ctx.Method(), host: ctx.Host(), origin: ctx.Header("Origin"),
		fetchSite: ctx.Header("Sec-Fetch-Site"), csrf: ctx.Header(CSRFHeader),
	})
	switch {
	case errors.Is(err, errNoSession):
		return p, http.StatusUnauthorized, "login required"
	case errors.Is(err, errCSRF):
		return p, http.StatusForbidden, "the CSRF check failed"
	case err != nil:
		return p, http.StatusServiceUnavailable, "a required dependency is not available"
	}
	return Principal{Actor: "user:" + s.Sub, Session: &s, CSRF: csrfToken(tok), InstanceAdmin: s.InstanceAdmin}, 0, ""
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
			project = store.DefaultProjectPath
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
	p, err := d.ds.GetProject(ctx, job.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		return "", errNoParent
	}
	if err != nil {
		return "", err
	}
	return p.Path, nil
}
