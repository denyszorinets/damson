package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
)

// Project is a node of the project tree (ARCHITECTURE 12.1).
type Project struct {
	ID          string    `json:"id"`
	Path        string    `json:"path" doc:"The full path, such as infra/billing. It never changes."`
	ParentPath  string    `json:"parent_path,omitempty" doc:"Empty for a root project."`
	Description string    `json:"description,omitempty"`
	ManagedBy   string    `json:"managed_by" doc:"ui, or files:<source> when apply manages the project; a managed project refuses edits here."`
	Version     int       `json:"version" doc:"Send as if_version when updating."`
	MyRole      string    `json:"my_role,omitempty" doc:"The caller's role here: viewer, runner, editor or admin; absent for none."`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toProject(p store.Project, role string) Project {
	return Project{
		ID: p.ID, Path: p.Path, ParentPath: store.ParentPath(p.Path), Description: p.Description,
		ManagedBy: p.ManagedBy, Version: p.Version, MyRole: role, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

type projectPathInput struct {
	Path string `path:"path" maxLength:"600" doc:"The project path, percent-encoded when it has several segments (infra%2Fbilling)."`
}

type createProjectInput struct {
	Body struct {
		Path        string `json:"path" required:"true" maxLength:"600" doc:"1 to 8 segments of lowercase letters, digits and '-'. The parent must exist."`
		Description string `json:"description,omitempty" maxLength:"512"`
	}
}

type updateProjectInput struct {
	Path string `path:"path" maxLength:"600"`
	Body struct {
		Description *string `json:"description,omitempty" maxLength:"512"`
		IfVersion   *int    `json:"if_version,omitempty" minimum:"1" doc:"The version the caller read; 409 if it moved. Omitted: the version current when handled."`
	}
}

func registerProjects(api huma.API, d deps) {
	op := func(id, method, path, summary string, status int, errs ...int) huma.Operation {
		return huma.Operation{
			OperationID: id, Method: method, Path: path, Summary: summary, Tags: []string{"projects"},
			Security: adminSecurity, DefaultStatus: status,
			Errors: append([]int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable}, errs...),
		}
	}
	huma.Register(api, op("list-projects", http.MethodGet, "/projects", "List the projects the caller can see", 0),
		func(ctx context.Context, _ *struct{}) (*struct {
			Body struct {
				Items []Project `json:"items" doc:"Ordered by path."`
			}
		}, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			rows, err := d.ds.ListProjects(ctx)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			p, _ := PrincipalFrom(ctx)
			roles := d.projectRoles(ctx, p, rows)
			out := &struct {
				Body struct {
					Items []Project `json:"items" doc:"Ordered by path."`
				}
			}{}
			out.Body.Items = []Project{}
			for _, r := range rows {
				if role := roles[r.Path]; role != "" { // only what the caller may see
					out.Body.Items = append(out.Body.Items, toProject(r, role))
				}
			}
			return out, nil
		})
	huma.Register(api, op("create-project", http.MethodPost, "/projects", "Create a project", http.StatusCreated,
		http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity),
		func(ctx context.Context, in *createProjectInput) (*struct{ Body Project }, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			if err := store.ValidateProjectPath(in.Body.Path); err != nil {
				return nil, apiError(ctx, invalid("the project path is invalid", ProblemDetail{Message: pathProblem(in.Body.Path), Location: "body.path"}))
			}
			if err := d.requireToCreate(ctx, in.Body.Path); err != nil {
				return nil, apiError(ctx, err)
			}
			if parent := store.ParentPath(in.Body.Path); parent != "" {
				if _, err := d.ds.GetProjectByPath(ctx, parent); err != nil {
					return nil, apiError(ctx, parentProblem(err))
				}
			}
			id, err := store.NewID()
			if err != nil {
				return nil, apiError(ctx, err)
			}
			p, err := d.ds.CreateProject(ctx, id, in.Body.Path, in.Body.Description, d.now())
			if err != nil {
				return nil, apiError(ctx, err)
			}
			d.grants.reset() // a role may rest on a project that is now another
			return &struct{ Body Project }{toProject(p, store.RoleAdmin)}, nil
		})
	huma.Register(api, op("get-project", http.MethodGet, "/projects/{path}", "Get a project", 0, http.StatusNotFound),
		func(ctx context.Context, in *projectPathInput) (*struct{ Body Project }, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			p, err := d.ds.GetProjectByPath(ctx, in.Path)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			return &struct{ Body Project }{toProject(p, d.myRole(ctx, p.Path))}, nil
		})
	huma.Register(api, op("update-project", http.MethodPatch, "/projects/{path}", "Update a project's description", 0,
		http.StatusNotFound, http.StatusConflict),
		func(ctx context.Context, in *updateProjectInput) (*struct{ Body Project }, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			cur, err := d.ds.GetProjectByPath(ctx, in.Path)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			version, desc := cur.Version, cur.Description
			if in.Body.IfVersion != nil {
				version = *in.Body.IfVersion
			}
			if in.Body.Description != nil {
				desc = *in.Body.Description
			}
			p, err := d.ds.UpdateProject(ctx, cur.ID, desc, version, d.now())
			if err != nil {
				return nil, apiError(ctx, err)
			}
			return &struct{ Body Project }{toProject(p, d.myRole(ctx, p.Path))}, nil
		})
	huma.Register(api, op("delete-project", http.MethodDelete, "/projects/{path}", "Delete an empty project", http.StatusNoContent,
		http.StatusNotFound, http.StatusConflict),
		func(ctx context.Context, in *projectPathInput) (*struct{}, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			p, err := d.ds.GetProjectByPath(ctx, in.Path)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			if err := d.requireToCreate(ctx, p.Path); err != nil { // like create: admin on the parent
				return nil, apiError(ctx, err)
			}
			if err := d.ds.DeleteProject(ctx, p.ID, d.now()); err != nil {
				return nil, apiError(ctx, err)
			}
			d.grants.reset()
			return nil, nil
		})
}

// pathProblem says what is wrong with a project path without echoing it.
func pathProblem(path string) string {
	if strings.Count(path, "/") >= store.MaxProjectDepth {
		return "at most 8 segments"
	}
	return "each segment must be 1 to 63 lowercase letters, digits or '-', starting with a letter or digit"
}

// parentProblem turns a missing parent into a 422 that names the field.
func parentProblem(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return invalid("the parent project does not exist", ProblemDetail{Message: "the parent project does not exist", Location: "body.path"})
	}
	return err
}

// requireToCreate checks that the caller may create (or delete) the project at path: a root
// project needs an instance admin, any other needs the admin role on its parent.
func (d deps) requireToCreate(ctx context.Context, path string) error {
	parent := store.ParentPath(path)
	if parent == "" {
		if p, ok := PrincipalFrom(ctx); !ok || !p.InstanceAdmin {
			return errForbidden
		}
		return nil
	}
	return d.require(ctx, parent, "", store.RoleAdmin)
}

// myRole is the caller's role on a project, "" for none.
func (d deps) myRole(ctx context.Context, project string) string {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return ""
	}
	role, _ := d.roleOn(ctx, p, project, "")
	return role
}
