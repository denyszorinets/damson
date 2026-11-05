package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store"
)

// SecretBackend is what the secrets endpoints need from the secret backend.
// Values go in through Put and never come back out of this API.
type SecretBackend interface {
	Put(ctx context.Context, scope, name, typ string, fields map[string]string, ifVersion *int64) (meta store.SecretMeta, created bool, err error)
	List(ctx context.Context, scope, afterScope, afterName string, limit int) ([]store.SecretMeta, error)
	Delete(ctx context.Context, scope, name string) error
}

// SecretMeta describes a secret without its values.
type SecretMeta struct {
	Ref            string    `json:"ref" doc:"secret://<scope>/<name>; append #<field> to use one field."`
	Scope          string    `json:"scope"`
	Name           string    `json:"name"`
	Type           string    `json:"type"`
	Fields         []string  `json:"fields" doc:"Field names of the current version."`
	CurrentVersion int64     `json:"current_version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func toSecretMeta(m store.SecretMeta) SecretMeta {
	f := m.FieldNames
	if f == nil {
		f = []string{}
	}
	return SecretMeta{
		Ref: secrets.Ref{Scope: m.Scope, Name: m.Name}.Base(), Scope: m.Scope, Name: m.Name, Type: m.Type,
		Fields: f, CurrentVersion: m.CurrentVersion, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

type secretPath struct {
	Scope string `path:"scope" maxLength:"600" doc:"The scope, percent-encoded when it has several segments (infra%2Fbilling), or _inventory."`
	Name  string `path:"name" maxLength:"128"`
}

type listSecretsInput struct {
	Scope  string `query:"scope" maxLength:"600" doc:"Only this scope; empty lists every scope."`
	Cursor string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor."`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
}

type listSecretsOutput struct {
	Body struct {
		Items      []SecretMeta `json:"items"`
		NextCursor string       `json:"next_cursor,omitempty" doc:"Empty on the last page."`
	}
}

type putSecretInput struct {
	Scope string `path:"scope" maxLength:"600" doc:"The scope, percent-encoded when it has several segments (infra%2Fbilling), or _inventory."`
	Name  string `path:"name" maxLength:"128"`
	Body  struct {
		Type      string            `json:"type" required:"true" maxLength:"32" doc:"See GET /secret-types. Fixed when the secret is created."`
		Fields    map[string]string `json:"fields" required:"true" writeOnly:"true" doc:"Field values. Never returned by any endpoint."`
		IfVersion *int64            `json:"if_version,omitempty" minimum:"0" doc:"0 creates only; n requires current_version n; omitted writes a new version of whatever exists."`
	}
}

type putSecretOutput struct {
	Status int `json:"-"`
	Body   SecretMeta
}

type secretTypesOutput struct {
	Body struct {
		Items []secrets.Type `json:"items"`
	}
}

func registerSecrets(api huma.API, d deps) {
	guard := huma.Middlewares{requireAdmin(api, d)}
	huma.Register(api, huma.Operation{
		OperationID: "list-secrets", Method: http.MethodGet, Path: "/secrets",
		Summary: "List secret metadata", Tags: []string{"secrets"}, Security: adminSecurity, Middlewares: guard,
		Description: "Names, types, field names and versions. Values are never returned.",
		Errors:      []int{http.StatusUnauthorized, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *listSecretsInput) (*listSecretsOutput, error) {
		if err := d.secretsReady(); err != nil {
			return nil, apiError(ctx, err)
		}
		cur, err := decodeCursor(ctx, in.Cursor)
		if err != nil {
			return nil, err
		}
		afterScope, afterName, _ := strings.Cut(cur, "\x00")
		rows, err := d.secrets.List(ctx, in.Scope, afterScope, afterName, in.Limit+1)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		out := &listSecretsOutput{}
		out.Body.Items = []SecretMeta{}
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			last := rows[len(rows)-1]
			out.Body.NextCursor = encodeCursor(last.Scope + "\x00" + last.Name)
		}
		for _, m := range rows {
			out.Body.Items = append(out.Body.Items, toSecretMeta(m))
		}
		return out, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "put-secret", Method: http.MethodPut, Path: "/secrets/{scope}/{name}",
		Summary: "Create a secret or add a version", Tags: []string{"secrets"}, Security: adminSecurity, Middlewares: guard,
		Description: "201 when the secret is created, 200 when a version is added. The response never contains values.",
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *putSecretInput) (*putSecretOutput, error) {
		if err := d.secretsReady(); err != nil {
			return nil, apiError(ctx, err)
		}
		m, created, err := d.secrets.Put(ctx, in.Scope, in.Name, in.Body.Type, in.Body.Fields, in.Body.IfVersion)
		clear(in.Body.Fields)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		return &putSecretOutput{Status: status, Body: toSecretMeta(m)}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "delete-secret", Method: http.MethodDelete, Path: "/secrets/{scope}/{name}",
		Summary: "Delete a secret and all its versions", Tags: []string{"secrets"}, Security: adminSecurity, Middlewares: guard,
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *secretPath) (*struct{}, error) {
		if err := d.secretsReady(); err != nil {
			return nil, apiError(ctx, err)
		}
		if err := d.secrets.Delete(ctx, in.Scope, in.Name); err != nil {
			return nil, apiError(ctx, err)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "list-secret-types", Method: http.MethodGet, Path: "/secret-types",
		Summary: "The secret types and their fields", Tags: []string{"secrets"},
	}, func(context.Context, *struct{}) (*secretTypesOutput, error) {
		out := &secretTypesOutput{}
		out.Body.Items = secrets.Types()
		return out, nil
	})
}

// secretsReady is the error to answer with when secrets cannot be served.
func (d deps) secretsReady() error {
	if d.secrets == nil {
		return secrets.ErrDisabled
	}
	return nil
}
