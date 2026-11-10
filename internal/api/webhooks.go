package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

// Webhook starts runs of a job when its token is posted to.
type Webhook struct {
	ID             string         `json:"id"`
	JobID          string         `json:"job_id"`
	Name           string         `json:"name"`
	Options        map[string]any `json:"options" doc:"Static options every run starts with; the mapping's result is merged over them."`
	OptionsMapping string         `json:"options_mapping,omitempty" doc:"A CEL expression over body, headers and query that yields a map of options."`
	Revoked        bool           `json:"revoked"`
	RevokedAt      *time.Time     `json:"revoked_at,omitempty"`
	LastUsedAt     *time.Time     `json:"last_used_at,omitempty"`
	Version        int64          `json:"version"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// CreatedWebhook is a new webhook with its token, which is shown only here.
type CreatedWebhook struct {
	Webhook
	Token string `json:"token" doc:"The secret. POST to /hooks/{token}. Shown once; only its hash is stored."`
	Path  string `json:"path" doc:"/hooks/ plus the token."`
}

func toWebhook(w store.Webhook) Webhook {
	opts := map[string]any{}
	_ = json.Unmarshal(w.Options, &opts)
	return Webhook{
		ID: w.ID, JobID: w.JobID, Name: w.Name, Options: opts, OptionsMapping: w.OptionsMapping, Revoked: w.RevokedAt != nil,
		RevokedAt: w.RevokedAt, LastUsedAt: w.LastUsedAt, Version: w.Version, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

type webhookIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

type webhookBody struct{ Body Webhook }

type createdWebhookBody struct{ Body CreatedWebhook }

type listWebhooksInput struct {
	JobID   string `query:"job_id" maxLength:"64" doc:"Only the webhooks of this job."`
	Project string `query:"project" maxLength:"256" doc:"Only the webhooks of the jobs of this project and the projects below it. A user needs job_id or project; a project the caller holds no role on answers 404."`
	Cursor  string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor."`
	Limit   int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
}

type listWebhooksOutput struct {
	Body struct {
		Items      []Webhook `json:"items" doc:"Revoked webhooks are listed too."`
		NextCursor string    `json:"next_cursor,omitempty"`
	}
}

type createWebhookInput struct {
	Body struct {
		JobID          string         `json:"job_id" required:"true" maxLength:"64"`
		Name           string         `json:"name" required:"true" maxLength:"63"`
		Options        map[string]any `json:"options,omitempty"`
		OptionsMapping string         `json:"options_mapping,omitempty" maxLength:"4096"`
	}
}

func registerWebhooks(api huma.API, d deps, env *expr.Env) {
	op := func(id, method, path, summary string, status int, errs ...int) huma.Operation {
		return huma.Operation{
			OperationID: id, Method: method, Path: path, Summary: summary, Tags: []string{"webhooks"},
			Security: adminSecurity, DefaultStatus: status,
			Errors: append([]int{http.StatusUnauthorized, http.StatusServiceUnavailable}, errs...),
		}
	}
	huma.Register(api, op("list-webhooks", http.MethodGet, "/webhooks", "List webhooks", 0, http.StatusBadRequest), func(ctx context.Context, in *listWebhooksInput) (*listWebhooksOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		after, err := decodeCursor(ctx, in.Cursor)
		if err != nil {
			return nil, err
		}
		if err := d.checkTriggerList(ctx, in.JobID, in.Project); err != nil {
			return nil, err
		}
		var rows []store.Webhook
		if in.Project != "" {
			rows, err = d.ds.ListWebhooksByProject(ctx, in.Project, after, in.Limit+1)
		} else {
			rows, err = d.ds.ListWebhooks(ctx, in.JobID, after, in.Limit+1)
		}
		if err != nil {
			return nil, apiError(ctx, err)
		}
		out := &listWebhooksOutput{}
		out.Body.Items = []Webhook{}
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			out.Body.NextCursor = encodeCursor(rows[len(rows)-1].ID)
		}
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, toWebhook(r))
		}
		return out, nil
	})
	huma.Register(api, op("get-webhook", http.MethodGet, "/webhooks/{id}", "Get a webhook", 0, http.StatusNotFound), func(ctx context.Context, in *webhookIDInput) (*webhookBody, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		w, err := d.ds.GetWebhook(ctx, in.ID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		return &webhookBody{Body: toWebhook(w)}, nil
	})
	huma.Register(api, op("create-webhook", http.MethodPost, "/webhooks", "Create a webhook", http.StatusCreated,
		http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity), func(ctx context.Context, in *createWebhookInput) (*createdWebhookBody, error) {
		return createWebhook(ctx, d, env, in)
	})
	huma.Register(api, op("revoke-webhook", http.MethodDelete, "/webhooks/{id}", "Revoke a webhook", http.StatusNoContent, http.StatusNotFound), func(ctx context.Context, in *webhookIDInput) (*struct{}, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		if err := d.ds.RevokeWebhook(ctx, in.ID, d.now()); err != nil {
			return nil, apiError(ctx, err)
		}
		return nil, nil
	})
}

func createWebhook(ctx context.Context, d deps, env *expr.Env, in *createWebhookInput) (*createdWebhookBody, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	b := in.Body
	if err := d.requireOnJob(ctx, b.JobID, store.RoleAdmin); err != nil {
		return nil, apiError(ctx, err)
	}
	if b.OptionsMapping != "" {
		if _, err := env.Compile(b.OptionsMapping); err != nil {
			return nil, apiError(ctx, invalid("the options mapping does not compile", ProblemDetail{Message: err.Error(), Location: "body.options_mapping"}))
		}
	}
	opts, err := json.Marshal(nonNilMap(b.Options))
	if err != nil {
		return nil, apiError(ctx, fmt.Errorf("encoding options: %w", err))
	}
	// Static options alone are checked now. With a mapping the final options
	// exist only per request, and are checked then.
	_, _, spec, err := d.loadSpec(ctx, b.JobID, nil)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	if b.OptionsMapping == "" {
		if _, err := resolveOptions(spec, b.Options); err != nil {
			return nil, apiError(ctx, err)
		}
	} else if bad := engine.SecretOptionViolations(spec.Spec.Options, b.Options); len(bad) > 0 {
		// Secret options must be references even when the rest is checked per
		// request: the options are stored.
		return nil, apiError(ctx, invalid("the options are invalid", secretFields(bad)...))
	}
	id, err := store.NewID()
	if err != nil {
		return nil, apiError(ctx, err)
	}
	token, hash, err := tokens.GenerateWithPrefix(rand.Reader, "")
	if err != nil {
		return nil, apiError(ctx, err)
	}
	created, err := d.ds.CreateWebhook(ctx, store.NewWebhook{
		Webhook:   store.Webhook{ID: id, JobID: b.JobID, Name: b.Name, Options: opts, OptionsMapping: b.OptionsMapping},
		TokenHash: hash,
	}, d.now())
	if err != nil {
		return nil, apiError(ctx, err)
	}
	return &createdWebhookBody{Body: CreatedWebhook{Webhook: toWebhook(created), Token: token, Path: hookPrefix + token}}, nil
}
