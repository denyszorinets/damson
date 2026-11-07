package api

import (
	"context"
	"crypto/rand"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

// ServiceAccount is a non-human identity that owns API tokens.
type ServiceAccount struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// ServiceToken is the metadata of a service account token. The secret itself
// is shown once, when the token is created.
type ServiceToken struct {
	ID               string     `json:"id"`
	ServiceAccountID string     `json:"service_account_id"`
	Name             string     `json:"name"`
	Project          string     `json:"project" doc:"The project the token covers with its descendants; empty covers every project."`
	Role             string     `json:"role" doc:"viewer reads, runner also triggers and cancels, editor also changes jobs."`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	Revoked          bool       `json:"revoked"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// CreatedServiceToken is a new token with its secret, shown only here.
type CreatedServiceToken struct {
	ServiceToken
	Token string `json:"token" doc:"Send as Authorization: Bearer <token>. Shown once; only its hash is stored."`
}

func toServiceAccount(a store.ServiceAccount) ServiceAccount {
	return ServiceAccount{ID: a.ID, Name: a.Name, Description: a.Description, CreatedAt: a.CreatedAt}
}

func toServiceToken(t store.ServiceToken) ServiceToken {
	return ServiceToken{
		ID: t.ID, ServiceAccountID: t.ServiceAccountID, Name: t.Name, Project: t.Project, Role: t.Role,
		ExpiresAt: t.ExpiresAt, Revoked: t.RevokedAt != nil, RevokedAt: t.RevokedAt, LastUsedAt: t.LastUsedAt, CreatedAt: t.CreatedAt,
	}
}

type serviceAccountIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

type createServiceAccountInput struct {
	Body struct {
		Name        string `json:"name" required:"true" minLength:"1" maxLength:"63" pattern:"^[a-z][a-z0-9_-]*$"`
		Description string `json:"description,omitempty" maxLength:"512"`
	}
}

type listServiceAccountsInput struct {
	Cursor string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor."`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
}

type createServiceTokenInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Name      string     `json:"name" required:"true" minLength:"1" maxLength:"63"`
		Project   string     `json:"project" required:"true" maxLength:"128" doc:"A project name, or empty for every project."`
		Role      string     `json:"role" required:"true" enum:"viewer,runner,editor"`
		ExpiresAt *time.Time `json:"expires_at,omitempty" doc:"Absent: the token does not expire. Must be in the future."`
	}
}

type tokenIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

func registerServiceAccounts(api huma.API, d deps) {
	guard := huma.Middlewares{requireAdmin(api, d)}
	op := func(id, method, path, summary string, status int, errs ...int) huma.Operation {
		return huma.Operation{
			OperationID: id, Method: method, Path: path, Summary: summary, Tags: []string{"service-accounts"},
			Security: adminSecurity, Middlewares: guard, DefaultStatus: status,
			Errors: append([]int{http.StatusUnauthorized, http.StatusServiceUnavailable}, errs...),
		}
	}
	huma.Register(api, op("list-service-accounts", http.MethodGet, "/service-accounts", "List service accounts", 0, http.StatusBadRequest),
		func(ctx context.Context, in *listServiceAccountsInput) (*struct {
			Body struct {
				Items      []ServiceAccount `json:"items"`
				NextCursor string           `json:"next_cursor,omitempty"`
			}
		}, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			after, err := decodeCursor(ctx, in.Cursor)
			if err != nil {
				return nil, err
			}
			rows, err := d.ds.ListServiceAccounts(ctx, after, in.Limit+1)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			out := &struct {
				Body struct {
					Items      []ServiceAccount `json:"items"`
					NextCursor string           `json:"next_cursor,omitempty"`
				}
			}{}
			out.Body.Items = []ServiceAccount{}
			if len(rows) > in.Limit {
				rows = rows[:in.Limit]
				out.Body.NextCursor = encodeCursor(rows[len(rows)-1].ID)
			}
			for _, r := range rows {
				out.Body.Items = append(out.Body.Items, toServiceAccount(r))
			}
			return out, nil
		})
	huma.Register(api, op("create-service-account", http.MethodPost, "/service-accounts", "Create a service account", http.StatusCreated, http.StatusConflict),
		func(ctx context.Context, in *createServiceAccountInput) (*struct{ Body ServiceAccount }, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			id, err := store.NewID()
			if err != nil {
				return nil, apiError(ctx, err)
			}
			a, err := d.ds.CreateServiceAccount(ctx, store.ServiceAccount{ID: id, Name: in.Body.Name, Description: in.Body.Description}, d.now())
			if err != nil {
				return nil, apiError(ctx, err)
			}
			return &struct{ Body ServiceAccount }{toServiceAccount(a)}, nil
		})
	huma.Register(api, op("get-service-account", http.MethodGet, "/service-accounts/{id}", "Get a service account", 0, http.StatusNotFound),
		func(ctx context.Context, in *serviceAccountIDInput) (*struct{ Body ServiceAccount }, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			a, err := d.ds.GetServiceAccount(ctx, in.ID)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			return &struct{ Body ServiceAccount }{toServiceAccount(a)}, nil
		})
	huma.Register(api, op("delete-service-account", http.MethodDelete, "/service-accounts/{id}", "Delete a service account and its tokens", http.StatusNoContent, http.StatusNotFound),
		func(ctx context.Context, in *serviceAccountIDInput) (*struct{}, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			if err := d.ds.DeleteServiceAccount(ctx, in.ID, d.now()); err != nil {
				return nil, apiError(ctx, err)
			}
			return nil, nil
		})
	huma.Register(api, op("list-service-tokens", http.MethodGet, "/service-accounts/{id}/tokens", "List the tokens of a service account", 0, http.StatusNotFound),
		func(ctx context.Context, in *serviceAccountIDInput) (*struct {
			Body struct {
				Items []ServiceToken `json:"items" doc:"Revoked tokens are listed too."`
			}
		}, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			if _, err := d.ds.GetServiceAccount(ctx, in.ID); err != nil {
				return nil, apiError(ctx, err)
			}
			rows, err := d.ds.ListServiceTokens(ctx, in.ID)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			out := &struct {
				Body struct {
					Items []ServiceToken `json:"items" doc:"Revoked tokens are listed too."`
				}
			}{}
			out.Body.Items = []ServiceToken{}
			for _, r := range rows {
				out.Body.Items = append(out.Body.Items, toServiceToken(r))
			}
			return out, nil
		})
	huma.Register(api, op("create-service-token", http.MethodPost, "/service-accounts/{id}/tokens", "Create a token", http.StatusCreated, http.StatusNotFound, http.StatusUnprocessableEntity),
		func(ctx context.Context, in *createServiceTokenInput) (*struct{ Body CreatedServiceToken }, error) {
			return createServiceToken(ctx, d, in)
		})
	huma.Register(api, op("revoke-service-token", http.MethodDelete, "/tokens/{id}", "Revoke a token", http.StatusNoContent, http.StatusNotFound),
		func(ctx context.Context, in *tokenIDInput) (*struct{}, error) {
			if err := d.ready(); err != nil {
				return nil, apiError(ctx, err)
			}
			if err := d.ds.RevokeServiceToken(ctx, in.ID, d.now()); err != nil {
				return nil, apiError(ctx, err)
			}
			return nil, nil
		})
}

func createServiceToken(ctx context.Context, d deps, in *createServiceTokenInput) (*struct{ Body CreatedServiceToken }, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	b := in.Body
	var fields []ProblemDetail
	if b.ExpiresAt != nil && !b.ExpiresAt.After(d.now()) {
		fields = append(fields, ProblemDetail{Message: "must be in the future", Location: "body.expires_at"})
	}
	if b.Project != "" {
		if _, err := d.ds.GetProjectByName(ctx, b.Project); err != nil {
			fields = append(fields, ProblemDetail{Message: "no such project", Location: "body.project"})
		}
	}
	if len(fields) > 0 {
		return nil, apiError(ctx, invalid("the token is invalid", fields...))
	}
	id, err := store.NewID()
	if err != nil {
		return nil, apiError(ctx, err)
	}
	secret, hash, err := tokens.GenerateWithPrefix(rand.Reader, ServiceTokenPrefix)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	created, err := d.ds.CreateServiceToken(ctx, store.NewServiceToken{
		ServiceToken: store.ServiceToken{ID: id, ServiceAccountID: in.ID, Name: b.Name, Project: b.Project, Role: b.Role, ExpiresAt: b.ExpiresAt},
		TokenHash:    hash,
	}, d.now())
	if err != nil {
		return nil, apiError(ctx, err)
	}
	return &struct{ Body CreatedServiceToken }{CreatedServiceToken{ServiceToken: toServiceToken(created), Token: secret}}, nil
}
