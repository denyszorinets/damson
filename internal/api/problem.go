// Package api is the controller's HTTP server: health endpoints, the single
// error-to-problem+json mapping, request logging and the embedded web UI.
package api

import (
	"context"
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
	// Errors lists per-field validation failures (Huma request validation).
	Errors []ProblemDetail `json:"errors,omitempty"`
}

// ProblemDetail is one validation failure inside a Problem.
type ProblemDetail struct {
	Message  string `json:"message,omitempty"`
	Location string `json:"location,omitempty"`
}

// Error implements error, and with GetStatus huma.StatusError, so a Problem
// can be returned from a Huma handler.
func (p *Problem) Error() string { return p.Detail }

// GetStatus implements huma.StatusError.
func (p *Problem) GetStatus() int { return p.Status }

// ContentType makes Huma serve a Problem as application/problem+json.
func (p *Problem) ContentType(string) string { return ProblemContentType }

// Problem type URIs. They are URNs, not URLs, so they never need to resolve.
const (
	typeNotFound    = "urn:damson:problem:not-found"
	typeConflict    = "urn:damson:problem:conflict"
	typeStale       = "urn:damson:problem:stale"
	typeUnavailable = "urn:damson:problem:unavailable"
	typeMethod      = "urn:damson:problem:method-not-allowed"
	typeInternal    = "urn:damson:problem:internal"
	typeValidation  = "urn:damson:problem:validation"
	typeRateLimited = "urn:damson:problem:rate-limited"
	// TypeUnauthorized and TypeForbidden are used by components that mount
	// their own handlers (the agent gateway) and answer before an upgrade.
	TypeUnauthorized = "urn:damson:problem:unauthorized"
	TypeForbidden    = "urn:damson:problem:forbidden"
	TypeBadRequest   = "urn:damson:problem:bad-request"
)

// errRunFinished marks a cancel of a run that already reached a terminal
// status.
var errRunFinished = errors.New("run already finished")

// errTooManyStreams marks a refused SSE stream: the server is at its limit.
var errTooManyStreams = errors.New("too many open streams")

// invalidError is a request the API understood but rejects (422). Fields
// locate each problem; they never carry request values.
type invalidError struct {
	detail string
	fields []ProblemDetail
}

func (e *invalidError) Error() string { return e.detail }

func invalid(detail string, fields ...ProblemDetail) error {
	return &invalidError{detail: detail, fields: fields}
}

// errUnavailable marks a dependency (database) that cannot serve requests.
var errUnavailable = errors.New("service unavailable")

// problemFor is the one place that maps an error to a Problem. Detail text for
// unrecognized errors is deliberately generic: internals (DSNs, SQL) must not
// reach clients.
func problemFor(err error) Problem {
	var inv *invalidError
	if errors.As(err, &inv) {
		return Problem{Type: typeValidation, Title: "Unprocessable Entity", Status: http.StatusUnprocessableEntity, Detail: inv.detail, Errors: inv.fields}
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return Problem{Type: typeNotFound, Title: "Not Found", Status: http.StatusNotFound, Detail: "the requested resource does not exist"}
	case errors.Is(err, store.ErrConflict):
		return Problem{Type: typeConflict, Title: "Conflict", Status: http.StatusConflict, Detail: "the resource already exists"}
	case errors.Is(err, store.ErrStale):
		return Problem{Type: typeStale, Title: "Stale Resource", Status: http.StatusConflict, Detail: "the resource changed since it was read; reload and retry"}
	case errors.Is(err, errRunFinished):
		return Problem{Type: typeConflict, Title: "Conflict", Status: http.StatusConflict, Detail: "the run has already finished"}
	case errors.Is(err, store.ErrInvalid):
		return Problem{Type: typeValidation, Title: "Unprocessable Entity", Status: http.StatusUnprocessableEntity, Detail: "the request was rejected as invalid"}
	case errors.Is(err, errTooManyStreams):
		return Problem{Type: typeUnavailable, Title: "Service Unavailable", Status: http.StatusServiceUnavailable, Detail: "too many open streams; retry later"}
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
	logFailure(loggerFrom(r), p, err)
	writeProblem(w, r, p)
}

func logFailure(log *slog.Logger, p Problem, err error) {
	switch {
	case p.Status == http.StatusInternalServerError:
		log.Error("request failed", "status", p.Status, "error", err)
	case p.Status >= http.StatusBadRequest:
		log.Warn("request failed", "status", p.Status, "error", err)
	}
}

// apiError is WriteError for Huma handlers: it maps err with problemFor and
// returns the Problem for Huma to write.
func apiError(ctx context.Context, err error) error {
	p := problemFor(err)
	logFailure(loggerFromCtx(ctx), p, err)
	p.RequestID = requestIDFromCtx(ctx)
	return &p
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
