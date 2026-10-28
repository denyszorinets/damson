package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"sync"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/denyszorinets/damson/internal/store"
)

// APIPrefix is where the typed API is mounted; the OpenAPI document is at
// APIPrefix + "/openapi.json".
const APIPrefix = "/api/v1"

// DataStore is what the typed endpoints read from the store.
type DataStore interface {
	ListAgents(ctx context.Context, cursor string, limit int) (store.AgentPage, error)
	GetAgent(ctx context.Context, id string) (store.Agent, error)
	ListPools(ctx context.Context) ([]store.Pool, error)
}

// setErrorModel points Huma's own errors (request validation, 4xx) at
// Problem, so problemFor stays the only error-to-problem+json mapping. Huma
// keeps the hook in package variables, hence the Once.
var setErrorModel sync.Once

func installErrorModel() {
	setErrorModel.Do(func() {
		huma.NewError = newHumaError
		huma.NewErrorWithContext = func(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
			e := newHumaError(status, msg, errs...).(*Problem)
			e.RequestID = requestIDFromCtx(ctx.Context())
			return e
		}
	})
}

func newHumaError(status int, msg string, errs ...error) huma.StatusError {
	p := &Problem{Type: typeForStatus(status), Title: http.StatusText(status), Status: status, Detail: msg}
	for _, e := range errs {
		if e == nil {
			continue
		}
		d := ProblemDetail{Message: e.Error()}
		if ed, ok := e.(huma.ErrorDetailer); ok {
			x := ed.ErrorDetail()
			d = ProblemDetail{Message: x.Message, Location: x.Location}
		}
		p.Errors = append(p.Errors, d)
	}
	return p
}

func typeForStatus(status int) string {
	switch {
	case status == http.StatusNotFound:
		return typeNotFound
	case status == http.StatusUnauthorized:
		return TypeUnauthorized
	case status == http.StatusForbidden:
		return TypeForbidden
	case status == http.StatusConflict:
		return typeConflict
	case status == http.StatusTooManyRequests:
		return typeRateLimited
	case status == http.StatusMethodNotAllowed:
		return typeMethod
	case status == http.StatusUnprocessableEntity:
		return typeValidation
	case status >= 500:
		return typeInternal
	default:
		return TypeBadRequest
	}
}

// newHuma registers the typed API on mux under APIPrefix.
func newHuma(mux *http.ServeMux, ds DataStore, pc PluginCatalog) huma.API {
	installErrorModel()
	cfg := huma.DefaultConfig("Damson API", "1")
	cfg.Info.Description = "Damson controller HTTP API (ARCHITECTURE section 14)."
	cfg.OpenAPIPath = "/openapi"
	cfg.DocsPath = "" // the bundled docs page loads assets from a CDN
	cfg.SchemasPath = ""
	cfg.CreateHooks = nil // no $schema link headers or fields
	api := humago.NewWithPrefix(mux, APIPrefix, cfg)
	registerAgents(api, ds)
	registerPools(api, ds)
	registerPlugins(api, pc)
	return api
}

// unavailable answers every typed endpoint when no store is configured.
func unavailable(ctx context.Context) error {
	return apiError(ctx, errUnavailable)
}

// encodeCursor makes a store keyset value opaque to clients.
func encodeCursor(key string) string {
	if key == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

// decodeCursor reverses encodeCursor. A cursor that is not valid base64url is
// a 400 problem.
func decodeCursor(ctx context.Context, c string) (string, error) {
	if c == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || len(b) == 0 {
		p := newHumaError(http.StatusBadRequest, "invalid cursor").(*Problem)
		p.RequestID = requestIDFromCtx(ctx)
		return "", p
	}
	return string(b), nil
}
