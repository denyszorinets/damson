// Package api is the controller's HTTP server: health endpoints, the single
// error-to-problem+json mapping, request logging and the embedded web UI.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/denyszorinets/damson/internal/store"
)

// ProblemContentType is the RFC 9457 media type.
const ProblemContentType = "application/problem+json"

// Problem is an RFC 9457 problem details object.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// Problem type URIs. They are URNs, not URLs, so they never need to resolve.
const (
	typeNotFound    = "urn:damson:problem:not-found"
	typeConflict    = "urn:damson:problem:conflict"
	typeStale       = "urn:damson:problem:stale"
	typeUnavailable = "urn:damson:problem:unavailable"
	typeMethod      = "urn:damson:problem:method-not-allowed"
	typeInternal    = "urn:damson:problem:internal"
	// TypeUnauthorized and TypeForbidden are used by components that mount
	// their own handlers (the agent gateway) and answer before an upgrade.
	TypeUnauthorized = "urn:damson:problem:unauthorized"
	TypeForbidden    = "urn:damson:problem:forbidden"
	TypeBadRequest   = "urn:damson:problem:bad-request"
)

// errUnavailable marks a dependency (database) that cannot serve requests.
var errUnavailable = errors.New("service unavailable")

// problemFor is the one place that maps an error to a Problem. Detail text for
// unrecognized errors is deliberately generic: internals (DSNs, SQL) must not
// reach clients.
func problemFor(err error) Problem {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return Problem{Type: typeNotFound, Title: "Not Found", Status: http.StatusNotFound, Detail: "the requested resource does not exist"}
	case errors.Is(err, store.ErrConflict):
		return Problem{Type: typeConflict, Title: "Conflict", Status: http.StatusConflict, Detail: "the resource already exists"}
	case errors.Is(err, store.ErrStale):
		return Problem{Type: typeStale, Title: "Stale Resource", Status: http.StatusConflict, Detail: "the resource changed since it was read; reload and retry"}
	case errors.Is(err, errUnavailable):
		return Problem{Type: typeUnavailable, Title: "Service Unavailable", Status: http.StatusServiceUnavailable, Detail: "a required dependency is not available"}
	default:
		return Problem{Type: typeInternal, Title: "Internal Server Error", Status: http.StatusInternalServerError, Detail: "an unexpected error occurred"}
	}
}

// WriteError maps err to a problem+json response. Server-side failures are
// logged with the request id; the client only sees the generic problem.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	p := problemFor(err)
	switch {
	case p.Status == http.StatusInternalServerError:
		loggerFrom(r).Error("request failed", "status", p.Status, "error", err)
	case p.Status >= http.StatusBadRequest:
		loggerFrom(r).Warn("request failed", "status", p.Status, "error", err)
	}
	writeProblem(w, r, p)
}

// writeProblem sends p; used directly for routing errors that carry no error.
func writeProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	p.RequestID = w.Header().Get(requestIDHeader)
	w.Header().Set("Content-Type", ProblemContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	if err := json.NewEncoder(w).Encode(p); err != nil {
		loggerFrom(r).Debug("writing problem", slog.Any("error", err))
	}
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, Problem{Type: typeNotFound, Title: "Not Found", Status: http.StatusNotFound, Detail: "no such API route"})
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	writeProblem(w, r, Problem{Type: typeMethod, Title: "Method Not Allowed", Status: http.StatusMethodNotAllowed, Detail: "use " + allow})
}

// WriteProblem sends a problem+json response with the given status. It is for
// handlers mounted from other packages that need a specific 4xx answer; errors
// from the store go through WriteError.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, typ, detail string) {
	writeProblem(w, r, Problem{Type: typ, Title: http.StatusText(status), Status: status, Detail: detail})
}
