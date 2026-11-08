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
	Version     int       `json:"version" doc:"Send as if_version when updating."`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toProject(p store.Project) Project {
	return Project{
		ID: p.ID, Path: p.Path, ParentPath: store.ParentPath(p.Path), Description: p.Description,
		Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
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
	guard := huma.Middlewares{requireAdmin(api, d)}
	op := func(id, method, path, summary string, status int, errs ...int) huma.Operation {
		return huma.Operation{
			OperationID: id, Method: method, Path: path, Summary: summary, Tags: []string{"projects"},
			Security: adminSecurity, Middlewares: guard, DefaultStatus: status,
			Errors: append([]int{http.StatusUnauthorized, http.StatusServiceUnavailable}, errs...),
		}
	}
	huma.Register(api, op("list-projects", http.MethodGet, "/projects", "List projects", 0),
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
			out := &struct {
				Body struct {
					Items []Project `json:"items" doc:"Ordered by path."`
				}
			}{}
			out.Body.Items = make([]Project, len(rows))
			for i, r := range rows {
				out.Body.Items[i] = toProject(r)
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
			return &struct{ Body Project }{toProject(p)}, nil
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
			return &struct{ Body Project }{toProject(p)}, nil
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
			return &struct{ Body Project }{toProject(p)}, nil
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
			if err := d.ds.DeleteProject(ctx, p.ID, d.now()); err != nil {
				return nil, apiError(ctx, err)
			}
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
