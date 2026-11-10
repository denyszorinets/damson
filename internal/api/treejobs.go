package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
)

type treeJobsInput struct {
	Scope  string `query:"scope" maxLength:"600" doc:"A project path: list the jobs of that project and the projects below it. Absent lists every project the caller can see."`
	Cursor string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor."`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
}

type treeJobsOutput struct {
	Body struct {
		Items      []Job  `json:"items" doc:"Ordered by project path, then job id."`
		NextCursor string `json:"next_cursor,omitempty"`
	}
}

func registerTreeJobs(api huma.API, d deps) {
	huma.Register(api, huma.Operation{
		OperationID: "list-scoped-jobs", Method: http.MethodGet, Path: "/tree/jobs", Tags: []string{"jobs"}, Security: adminSecurity,
		Summary:     "Jobs of the projects the caller can see, optionally of one project and below",
		Description: "Only jobs in projects the caller holds a role on are listed. A scope the caller cannot see is 404, like one that does not exist. Jobs reached only through a rule of the job itself are listed by /tree/shared.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *treeJobsInput) (*treeJobsOutput, error) {
		return listScopedJobs(ctx, d, in)
	})
}

// scopeOf is what the caller sees. A request that passed the authorization
// middleware without a principal (login off, no token required) sees everything,
// as it does on every other open route.
func (d deps) scopeOf(ctx context.Context) (visibleScope, error) {
	p, ok := PrincipalFrom(ctx)
	if ok {
		return d.visibleScope(ctx, p)
	}
	all, err := d.ds.ListProjects(ctx)
	if err != nil {
		return visibleScope{}, err
	}
	v := visibleScope{projects: all, roles: make(map[string]string, len(all))}
	for _, pr := range all {
		v.roles[pr.Path] = store.RoleAdmin
	}
	return v, nil
}

func inScope(path, scope string) bool {
	return scope == "" || path == scope || strings.HasPrefix(path, scope+"/")
}

func listScopedJobs(ctx context.Context, d deps, in *treeJobsInput) (*treeJobsOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	cursor, err := decodeCursor(ctx, in.Cursor)
	if err != nil {
		return nil, err
	}
	curPath, curID, _ := strings.Cut(cursor, "\x00")
	v, err := d.scopeOf(ctx)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	if in.Scope != "" && !v.sees(in.Scope) {
		return nil, apiError(ctx, store.ErrNotFound) // no role looks like no project
	}
	limit := store.ClampLimit(in.Limit)
	out := &treeJobsOutput{}
	out.Body.Items = []Job{}
	names := make(map[string]string, len(v.projects))
	for _, pr := range v.projects {
		names[pr.ID] = pr.Path
	}
	for _, pr := range v.projects { // ordered by path
		if !inScope(pr.Path, in.Scope) || pr.Path < curPath {
			continue
		}
		after := ""
		if pr.Path == curPath {
			after = curID
		}
		for len(out.Body.Items) <= limit {
			page, err := d.ds.ListJobs(ctx, pr.ID, after, limit+1-len(out.Body.Items))
			if err != nil {
				return nil, apiError(ctx, err)
			}
			for _, j := range page.Jobs {
				out.Body.Items = append(out.Body.Items, toJob(j, names))
			}
			if page.NextCursor == "" {
				break
			}
			after = page.NextCursor
		}
		if len(out.Body.Items) > limit {
			break
		}
	}
	if len(out.Body.Items) > limit {
		out.Body.Items = out.Body.Items[:limit]
		last := out.Body.Items[limit-1]
		out.Body.NextCursor = encodeCursor(last.Project + "\x00" + last.ID)
	}
	return out, nil
}
