package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
)

// AccessRule grants a role to whoever a CEL expression is true for. The
// expression sees claims (the raw id token claims) and user (sub, email, name,
// groups).
type AccessRule struct {
	Role string `json:"role" required:"true" enum:"viewer,runner,editor,admin" doc:"On a job only viewer, runner and editor are allowed."`
	Rule string `json:"rule" required:"true" minLength:"1" maxLength:"2048" doc:"A CEL expression that yields a boolean, for example 'billing-ops' in claims.groups."`
}

// AccessRules is a rule list with the version to send back as if_version.
type AccessRules struct {
	Rules   []AccessRule `json:"rules"`
	Version int          `json:"version" doc:"Send as if_version on the next change. A job with no rules has version 0."`
}

type putAccessBody struct {
	Rules     []AccessRule `json:"rules" required:"true" maxItems:"50" doc:"Replaces the whole list; empty removes every rule."`
	IfVersion *int         `json:"if_version,omitempty" minimum:"0" doc:"The version the caller read; 409 if it moved. Omitted: the version current when handled."`
}

type projectAccessPut struct {
	Path string `path:"path" maxLength:"600"`
	Body putAccessBody
}

type jobAccessPut struct {
	ID   string `path:"id" maxLength:"64"`
	Body putAccessBody
}

func toStoreRules(in []AccessRule) []store.AccessRule {
	out := make([]store.AccessRule, len(in))
	for i, r := range in {
		out[i] = store.AccessRule{Role: r.Role, Rule: r.Rule}
	}
	return out
}

func toAPIRules(in []store.AccessRule) []AccessRule {
	out := make([]AccessRule, len(in))
	for i, r := range in {
		out[i] = AccessRule{Role: r.Role, Rule: r.Rule}
	}
	return out
}

// checkRules compiles every rule and checks the roles allowed at this level; a
// problem is a 422 that names the rule by position, never quoting it.
func (d deps) checkRules(rules []AccessRule, maxRole string) error {
	var fields []ProblemDetail
	for i, r := range rules {
		if store.RoleRank(r.Role) > store.RoleRank(maxRole) {
			fields = append(fields, ProblemDetail{Message: "this role cannot be granted here", Location: fmt.Sprintf("body.rules[%d].role", i)})
		}
	}
	for _, fe := range d.policy.Validate(toStoreRules(rules)) {
		fields = append(fields, ProblemDetail{Message: fe.Message, Location: fmt.Sprintf("body.rules[%d].rule", fe.Index)})
	}
	if len(fields) > 0 {
		return invalid("the access rules are invalid", fields...)
	}
	return nil
}

func registerAccess(api huma.API, d deps) {
	op := func(id, method, path, summary string, tag string, errs ...int) huma.Operation {
		return huma.Operation{
			OperationID: id, Method: method, Path: path, Summary: summary, Tags: []string{tag}, Security: adminSecurity,
			Errors: append([]int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable}, errs...),
		}
	}
	huma.Register(api, op("get-project-access", http.MethodGet, "/projects/{path}/access-rules", "Get a project's access rules", "projects"),
		func(ctx context.Context, in *projectPathInput) (*struct{ Body AccessRules }, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			p, err := d.ds.GetProjectByPath(ctx, in.Path)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			return &struct{ Body AccessRules }{AccessRules{Rules: toAPIRules(p.AccessRules), Version: p.Version}}, nil
		})
	huma.Register(api, op("set-project-access", http.MethodPut, "/projects/{path}/access-rules", "Replace a project's access rules", "projects",
		http.StatusConflict, http.StatusUnprocessableEntity),
		func(ctx context.Context, in *projectAccessPut) (*struct{ Body AccessRules }, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			if err := d.checkRules(in.Body.Rules, store.RoleAdmin); err != nil {
				return nil, apiError(ctx, err)
			}
			cur, err := d.ds.GetProjectByPath(ctx, in.Path)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			version := cur.Version
			if in.Body.IfVersion != nil {
				version = *in.Body.IfVersion
			}
			p, err := d.ds.SetProjectAccessRules(ctx, cur.ID, toStoreRules(in.Body.Rules), version, d.now())
			if err != nil {
				return nil, apiError(ctx, err)
			}
			d.grants.reset()
			return &struct{ Body AccessRules }{AccessRules{Rules: toAPIRules(p.AccessRules), Version: p.Version}}, nil
		})
	huma.Register(api, op("get-job-access", http.MethodGet, "/jobs/{id}/access-rules", "Get a job's own access rules", "jobs"),
		func(ctx context.Context, in *jobIDInput) (*struct{ Body AccessRules }, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			if _, err := d.ds.GetJob(ctx, in.ID); err != nil {
				return nil, apiError(ctx, err)
			}
			a, err := d.ds.GetJobAccess(ctx, in.ID)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			return &struct{ Body AccessRules }{AccessRules{Rules: toAPIRules(a.Rules), Version: a.Version}}, nil
		})
	huma.Register(api, op("set-job-access", http.MethodPut, "/jobs/{id}/access-rules", "Replace a job's own access rules", "jobs",
		http.StatusConflict, http.StatusUnprocessableEntity),
		func(ctx context.Context, in *jobAccessPut) (*struct{ Body AccessRules }, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			if err := d.checkRules(in.Body.Rules, store.RoleEditor); err != nil {
				return nil, apiError(ctx, err)
			}
			if _, err := d.ds.GetJob(ctx, in.ID); err != nil {
				return nil, apiError(ctx, err)
			}
			cur, err := d.ds.GetJobAccess(ctx, in.ID)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			version := cur.Version
			if in.Body.IfVersion != nil {
				version = *in.Body.IfVersion
			}
			a, err := d.ds.SetJobAccessRules(ctx, in.ID, toStoreRules(in.Body.Rules), version, d.now())
			if err != nil {
				return nil, apiError(ctx, err)
			}
			d.grants.reset()
			return &struct{ Body AccessRules }{AccessRules{Rules: toAPIRules(a.Rules), Version: a.Version}}, nil
		})
}

// NodeScope is a project's own node scope.
type NodeScope struct {
	NodeScope string `json:"node_scope" doc:"A CEL expression over node (id, name, address, tags, attributes, pool, os, transport) that must be true for the project to use a node. Empty allows every node. A project is also held to the scopes of its ancestors."`
	Version   int    `json:"version" doc:"Send as if_version on the next change."`
}

type putNodeScopeInput struct {
	Path string `path:"path" maxLength:"600"`
	Body struct {
		NodeScope string `json:"node_scope" required:"true" maxLength:"2048"`
		IfVersion *int   `json:"if_version,omitempty" minimum:"1" doc:"The version the caller read; 409 if it moved. Omitted: the version current when handled."`
	}
}

func registerNodeScope(api huma.API, d deps) {
	op := func(id, method, summary string, errs ...int) huma.Operation {
		return huma.Operation{
			OperationID: id, Method: method, Path: "/projects/{path}/node-scope", Summary: summary, Tags: []string{"projects"}, Security: adminSecurity,
			Errors: append([]int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable}, errs...),
		}
	}
	huma.Register(api, op("get-project-node-scope", http.MethodGet, "Get a project's node scope"),
		func(ctx context.Context, in *projectPathInput) (*struct{ Body NodeScope }, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			p, err := d.ds.GetProjectByPath(ctx, in.Path)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			return &struct{ Body NodeScope }{NodeScope{NodeScope: p.NodeScope, Version: p.Version}}, nil
		})
	huma.Register(api, op("set-project-node-scope", http.MethodPut, "Set a project's node scope", http.StatusConflict, http.StatusUnprocessableEntity),
		func(ctx context.Context, in *putNodeScopeInput) (*struct{ Body NodeScope }, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			if in.Body.NodeScope != "" {
				if _, err := d.nodeScopeEnv.CompileRule(in.Body.NodeScope); err != nil {
					return nil, apiError(ctx, invalid("the node scope is invalid", ProblemDetail{Message: err.Error(), Location: "body.node_scope"}))
				}
			}
			cur, err := d.ds.GetProjectByPath(ctx, in.Path)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			version := cur.Version
			if in.Body.IfVersion != nil {
				version = *in.Body.IfVersion
			}
			p, err := d.ds.SetProjectNodeScope(ctx, cur.ID, in.Body.NodeScope, version, d.now())
			if err != nil {
				return nil, apiError(ctx, err)
			}
			return &struct{ Body NodeScope }{NodeScope{NodeScope: p.NodeScope, Version: p.Version}}, nil
		})
}
