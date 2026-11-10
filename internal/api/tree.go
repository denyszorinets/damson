package api

import (
	"context"
	"net/http"
	"sort"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
)

// maxJobRuleScan bounds the job-rule rows read to find the jobs a user reaches
// only through job-level rules (ADR-0075).
const maxJobRuleScan = 1000

// TreeProject is one project of the caller's tree (ADR-0075). The count fields
// are filled by the roll-ups slice and are absent until then.
type TreeProject struct {
	Path        string `json:"path" doc:"The full path."`
	Label       string `json:"label" doc:"The full path for a root of the caller's tree, else the last segment."`
	MyRole      string `json:"my_role" doc:"The caller's role: viewer, runner, editor or admin."`
	HasChildren bool   `json:"has_children" doc:"Whether the project has child projects the caller can see."`
	Jobs        *int   `json:"jobs,omitempty" doc:"Jobs in the project and the projects below it."`
	Failed24h   *int   `json:"failed_24h,omitempty" doc:"Runs that failed in the last 24 hours below this project."`
	Running     *int   `json:"running,omitempty" doc:"Runs now running below this project."`
}

// TreeJob is a job listed under its project.
type TreeJob struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	MyRole string `json:"my_role" doc:"The caller's role on the job."`
}

// SharedJob is a job the caller reaches only through a rule of the job itself.
// It names no project: the caller cannot see the project.
type SharedJob struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	MyRole string `json:"my_role"`
}

type treeInput struct {
	Parent     string `query:"parent" maxLength:"600" doc:"A project path to list the children and jobs of; absent lists the roots of the caller's tree."`
	Cursor     string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor (projects)."`
	JobsCursor string `query:"jobs_cursor" doc:"Opaque cursor from a previous page's jobs_next_cursor."`
	Limit      int    `query:"limit" default:"100" minimum:"1" maximum:"500" doc:"Page size, for projects and for jobs."`
}

type treeOutput struct {
	Body struct {
		Projects       []TreeProject `json:"projects" doc:"Ordered by path."`
		Jobs           []TreeJob     `json:"jobs" doc:"The parent's own jobs; empty for the roots."`
		NextCursor     string        `json:"next_cursor,omitempty"`
		JobsNextCursor string        `json:"jobs_next_cursor,omitempty"`
	}
}

type sharedOutput struct {
	Body struct {
		Items     []SharedJob `json:"items" doc:"Ordered by job id."`
		Truncated bool        `json:"truncated,omitempty" doc:"More jobs carry rules than were searched; some may be missing."`
	}
}

// visibleScope is what a principal may see of the project tree. The set is
// downward closed: a role on a project reaches its descendants, so everything
// below a visible node is visible and a roll-up over it never counts a hidden
// project (ADR-0075).
type visibleScope struct {
	projects []store.Project // visible, ordered by path
	roles    map[string]string
}

// visibleScope is the projects the principal holds a role on.
func (d deps) visibleScope(ctx context.Context, p Principal) (visibleScope, error) {
	all, err := d.ds.ListProjects(ctx)
	if err != nil {
		return visibleScope{}, err
	}
	roles := d.projectRoles(ctx, p, all)
	v := visibleScope{roles: roles}
	for _, pr := range all {
		if roles[pr.Path] != "" {
			v.projects = append(v.projects, pr)
		}
	}
	return v, nil
}

func (v visibleScope) sees(path string) bool { return v.roles[path] != "" }

// children are the visible projects directly below parent; the roots of the
// caller's tree when parent is empty: no ancestor stub stands in for a parent
// the caller cannot see.
func (v visibleScope) children(parent string) []store.Project {
	var out []store.Project
	for _, pr := range v.projects {
		up := store.ParentPath(pr.Path)
		if (parent == "" && !v.sees(up)) || (parent != "" && up == parent) {
			out = append(out, pr)
		}
	}
	return out
}

func (v visibleScope) hasChildren(path string) bool {
	for _, pr := range v.projects {
		if store.ParentPath(pr.Path) == path {
			return true
		}
	}
	return false
}

func registerTree(api huma.API, d deps) {
	huma.Register(api, huma.Operation{
		OperationID: "get-tree", Method: http.MethodGet, Path: "/tree", Tags: []string{"projects"}, Security: adminSecurity,
		Summary:     "One level of the caller's project tree",
		Description: "Lists the visible projects directly below parent (the roots of the caller's tree without one) and, below a parent, its jobs. Only what the caller may see appears; a parent the caller cannot see is 404, like one that does not exist.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *treeInput) (*treeOutput, error) {
		return getTree(ctx, d, in)
	})
	huma.Register(api, huma.Operation{
		OperationID: "get-shared-jobs", Method: http.MethodGet, Path: "/tree/shared", Tags: []string{"projects"}, Security: adminSecurity,
		Summary:     "Jobs the caller reaches only through a rule of the job itself",
		Description: "Jobs whose project the caller cannot see but whose own access rules grant a role. No project is named.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable},
	}, func(ctx context.Context, _ *struct{}) (*sharedOutput, error) {
		return getSharedJobs(ctx, d)
	})
}

func getTree(ctx context.Context, d deps, in *treeInput) (*treeOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	cursor, err := decodeCursor(ctx, in.Cursor)
	if err != nil {
		return nil, err
	}
	jobsCursor, err := decodeCursor(ctx, in.JobsCursor)
	if err != nil {
		return nil, err
	}
	p, _ := PrincipalFrom(ctx)
	v, err := d.visibleScope(ctx, p)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	if in.Parent != "" && !v.sees(in.Parent) {
		return nil, apiError(ctx, store.ErrNotFound) // no role looks like no project
	}
	limit := store.ClampLimit(in.Limit)
	out := &treeOutput{}
	out.Body.Projects = []TreeProject{}
	out.Body.Jobs = []TreeJob{}
	kids := v.children(in.Parent)
	sort.Slice(kids, func(i, j int) bool { return kids[i].Path < kids[j].Path })
	for _, k := range kids {
		if k.Path <= cursor {
			continue
		}
		if len(out.Body.Projects) == limit {
			out.Body.NextCursor = encodeCursor(out.Body.Projects[limit-1].Path)
			break
		}
		label := k.Path
		if in.Parent != "" {
			label = k.Path[len(in.Parent)+1:]
		}
		out.Body.Projects = append(out.Body.Projects, TreeProject{
			Path: k.Path, Label: label, MyRole: v.roles[k.Path], HasChildren: v.hasChildren(k.Path),
		})
	}
	if in.Parent == "" {
		return out, nil
	}
	parent, err := d.ds.GetProjectByPath(ctx, in.Parent)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	page, err := d.ds.ListJobs(ctx, parent.ID, jobsCursor, limit)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	for _, j := range page.Jobs {
		out.Body.Jobs = append(out.Body.Jobs, TreeJob{ID: j.ID, Name: j.Name, MyRole: v.roles[parent.Path]})
	}
	out.Body.JobsNextCursor = encodeCursor(page.NextCursor)
	return out, nil
}

func getSharedJobs(ctx context.Context, d deps) (*sharedOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	out := &sharedOutput{}
	out.Body.Items = []SharedJob{}
	p, _ := PrincipalFrom(ctx)
	// Only a logged-in user who is not an instance admin has jobs of this kind:
	// an admin and a service token already see through their project roles.
	if p.Session == nil || p.InstanceAdmin || d.policy == nil {
		return out, nil
	}
	v, err := d.visibleScope(ctx, p)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	all, err := d.ds.ListProjects(ctx)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	pathOf := make(map[string]string, len(all))
	for _, pr := range all {
		pathOf[pr.ID] = pr.Path
	}
	rows, err := d.ds.ListJobRules(ctx, maxJobRuleScan+1)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	if len(rows) > maxJobRuleScan {
		rows, out.Body.Truncated = rows[:maxJobRuleScan], true
	}
	id := d.identityOf(p.Session)
	for _, r := range rows {
		if v.sees(pathOf[r.ProjectID]) {
			continue // reached through its project: it is in the tree
		}
		if role := d.policy.Role(id, r.Rules); role != "" {
			out.Body.Items = append(out.Body.Items, SharedJob{ID: r.JobID, Name: r.Name, MyRole: role})
		}
	}
	return out, nil
}
