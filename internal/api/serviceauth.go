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

// subject says what a route addresses, so the principal's role can be checked
// against the target's project (and job).
type subject int

const (
	subjNone        subject = iota // no project: a token must cover every project
	subjJob                        // path {id} is a job id
	subjRun                        // path {id} is a run id
	subjExec                       // path {id} is an execution id
	subjProject                    // query "project" (default "default")
	subjListRuns                   // query "job" if set, else every project
	subjProjectPath                // path {path} is a project path
	subjHandler                    // the handler checks: the body names the target
	subjSchedule                   // path {id} is a schedule id; the target is its job
	subjWebhook                    // path {id} is a webhook id; the target is its job
	subjTriggerList                // query "job_id", else "project" (and below), else every project
)

// routePolicy says what a route needs. strict routes never run anonymously, even
// when login is off and require_token is not set.
type routePolicy struct {
	role   string
	on     subject
	strict bool
}

// routePolicies are the operations the role model governs, by operation id.
// Every other operation is guarded by requireAdmin (instance admin), is open on
// purpose (publicOps in the tests) or needs only a login. A test fails when an
// operation is in none of these.
var routePolicies = map[string]routePolicy{
	"validate-job":          {store.RoleViewer, subjNone, false},
	"create-job":            {store.RoleEditor, subjNone, false},
	"list-jobs":             {store.RoleViewer, subjProject, false},
	"get-job":               {store.RoleViewer, subjJob, false},
	"create-job-version":    {store.RoleEditor, subjJob, false},
	"list-job-versions":     {store.RoleViewer, subjJob, false},
	"get-job-version":       {store.RoleViewer, subjJob, false},
	"get-job-version-graph": {store.RoleViewer, subjJob, false},
	"trigger-run":           {store.RoleRunner, subjJob, false},
	"list-runs":             {store.RoleViewer, subjListRuns, false},
	"get-run":               {store.RoleViewer, subjRun, false},
	"get-run-graph":         {store.RoleViewer, subjRun, false},
	"stream-run-events":     {store.RoleViewer, subjRun, false},
	"cancel-run":            {store.RoleRunner, subjRun, false},
	"get-execution":         {store.RoleViewer, subjExec, false},
	"stream-execution-logs": {store.RoleViewer, subjExec, false},

	"list-audit-events":      {store.RoleAdmin, subjHandler, true},
	"list-projects":          {store.RoleViewer, subjHandler, true},
	"get-tree":               {store.RoleViewer, subjHandler, true},
	"get-shared-jobs":        {store.RoleViewer, subjHandler, true},
	"get-my-access":          {store.RoleViewer, subjHandler, true},
	"list-scoped-jobs":       {store.RoleViewer, subjHandler, false},
	"create-project":         {store.RoleAdmin, subjHandler, true},
	"get-project":            {store.RoleViewer, subjProjectPath, true},
	"update-project":         {store.RoleAdmin, subjProjectPath, true},
	"delete-project":         {store.RoleAdmin, subjProjectPath, true},
	"get-project-access":     {store.RoleAdmin, subjProjectPath, true},
	"set-project-access":     {store.RoleAdmin, subjProjectPath, true},
	"get-project-node-scope": {store.RoleAdmin, subjProjectPath, true},
	"set-project-node-scope": {store.RoleAdmin, subjProjectPath, true},
	"apply":                  {store.RoleEditor, subjHandler, true},
	"export":                 {store.RoleAdmin, subjHandler, true},
	"get-job-access":         {store.RoleAdmin, subjJob, true},
	"set-job-access":         {store.RoleAdmin, subjJob, true},

	// Schedules and webhooks belong to a job, so its project's roles govern them
	// (ADR-0076). A webhook token starts runs from outside, hence admin.
	"list-schedules":  {store.RoleViewer, subjTriggerList, true},
	"get-schedule":    {store.RoleViewer, subjSchedule, true},
	"create-schedule": {store.RoleEditor, subjHandler, true},
	"patch-schedule":  {store.RoleEditor, subjSchedule, true},
	"delete-schedule": {store.RoleEditor, subjSchedule, true},
	"list-webhooks":   {store.RoleViewer, subjTriggerList, true},
	"get-webhook":     {store.RoleViewer, subjWebhook, true},
	"create-webhook":  {store.RoleAdmin, subjHandler, true},
	"revoke-webhook":  {store.RoleAdmin, subjWebhook, true},
}

// covers reports whether a token scoped to scope may act in project: "" covers
// every project, otherwise the project path itself and its descendants.
func covers(scope, project string) bool {
	return scope == "" || project == scope || strings.HasPrefix(project, scope+"/")
}

// openOps need no login even when OIDC is on: static data and health.
var openOps = map[string]bool{"list-secret-types": true}

// authorize is the global middleware of the API (ADR-0060, ADR-0063, ADR-0064).
//
// For the operations in routePolicies: an instance admin (the admin token or a
// user the admin rule selected) may do everything. A service token needs a role
// at least the route's and a project scope that covers the target (out of scope
// answers 404, like a missing target). A user needs the role from the access
// rules: none at all answers 404, too low answers 403. Without credentials a
// request passes only for non-strict routes when OIDC is off and RequireToken
// is not set. With OIDC on, every operation that is neither open nor guarded by
// requireAdmin needs some principal.
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
		p, status, msg := d.authenticate(ctx, op.OperationID == meOperation)
		if status != 0 {
			deny(ctx, status, msg)
			return
		}
		if p.Actor == "" { // anonymous
			if pol.strict {
				deny(ctx, http.StatusUnauthorized, "a login or a token is required")
				return
			}
			next(huma.WithContext(ctx, store.WithAudit(ctx.Context(), store.AuditMeta{Actor: "anonymous", RequestID: requestIDFromCtx(ctx.Context())})))
			return
		}
		meta := store.AuditMeta{Actor: p.Actor, RequestID: requestIDFromCtx(ctx.Context())}
		if p.Session != nil {
			meta.ActorName = p.Session.Email
		}
		rctx := store.WithAudit(ctx.Context(), meta)
		rctx = context.WithValue(rctx, principalKey{}, p)
		hctx := huma.WithContext(ctx, rctx)
		status, msg = 0, ""
		if governed && !p.InstanceAdmin && pol.on != subjHandler {
			status, msg = d.checkRole(ctx, p, pol)
		}
		switch status {
		case 0:
			if p.Token != nil {
				_ = d.ds.TouchServiceToken(ctx.Context(), p.Token.ID, d.now()) // best effort
			}
			next(hctx)
		case http.StatusNotFound:
			denyNotFound(api, ctx)
		default:
			deny(ctx, status, msg)
		}
	}
}

// checkRole decides a governed route for a principal that is not an instance
// admin. 0 means allowed.
func (d deps) checkRole(ctx huma.Context, p Principal, pol routePolicy) (int, string) {
	t, err := d.target(ctx, pol.on)
	if err != nil {
		return http.StatusServiceUnavailable, "a required dependency is not available"
	}
	if p.Token != nil {
		if store.RoleRank(p.Token.Role) < store.RoleRank(pol.role) {
			return http.StatusForbidden, "the token's role does not allow this"
		}
		if !t.inTokenScope(p.Token.Project) {
			return http.StatusNotFound, ""
		}
		return 0, ""
	}
	switch t.kind {
	case tgtNone, tgtAll, tgtMissing: // the handler checks, filters or answers 404
		return 0, ""
	case tgtNoParent:
		return http.StatusNotFound, ""
	}
	role, err := d.roleOn(ctx.Context(), p, t.project, t.jobID)
	switch {
	case err != nil:
		return http.StatusServiceUnavailable, "a required dependency is not available"
	case role == "":
		return http.StatusNotFound, ""
	case store.RoleRank(role) < store.RoleRank(pol.role):
		return http.StatusForbidden, "your role does not allow this"
	}
	return 0, ""
}

const meOperation = "get-me"

// authenticate checks the credentials of a request: a Bearer token (the admin
// token or a service token), else with OIDC on the session cookie. A token that
// is sent always wins over a cookie. A zero principal and status 0 mean
// anonymous; otherwise status and msg are the refusal.
func (d deps) authenticate(ctx huma.Context, anonymousOK bool) (p Principal, status int, msg string) {
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
		return d.authenticateCookie(ctx, anonymousOK)
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
// anonymous unless RequireToken is set. anonymousOK (the /me route) stays
// anonymous either way, so the UI can tell it must ask for a token.
func (d deps) authenticateCookie(ctx huma.Context, anonymousOK bool) (p Principal, status int, msg string) {
	if d.oidc == nil {
		if d.requireToken && !anonymousOK {
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
