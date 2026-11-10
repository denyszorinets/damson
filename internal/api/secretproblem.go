package api

import (
	"errors"
	"net/http"

	"github.com/denyszorinets/damson/internal/secrets"
)

// secretProblem maps the secret backend's errors. The details name fields and
// refs, never values: the backend's messages are built that way.
func secretProblem(err error) (Problem, bool) {
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		return Problem{Type: typeNotFound, Title: "Not Found", Status: http.StatusNotFound, Detail: "no such secret"}, true
	case errors.Is(err, secrets.ErrVersionConflict):
		return Problem{Type: typeStale, Title: "Stale Resource", Status: http.StatusConflict, Detail: "the secret changed since it was read, or already exists"}, true
	case errors.Is(err, secrets.ErrTypeChange):
		return Problem{Type: typeConflict, Title: "Conflict", Status: http.StatusConflict, Detail: "the type of a secret cannot change"}, true
	case errors.Is(err, secrets.ErrTooLarge):
		return Problem{Type: typeValidation, Title: "Request Entity Too Large", Status: http.StatusRequestEntityTooLarge, Detail: err.Error()}, true
	case errors.Is(err, secrets.ErrBadFields), errors.Is(err, secrets.ErrBadRef), errors.Is(err, secrets.ErrBadType):
		return Problem{Type: typeValidation, Title: "Unprocessable Entity", Status: http.StatusUnprocessableEntity, Detail: err.Error()}, true
	case errors.Is(err, secrets.ErrReadOnly):
		return Problem{Type: typeConflict, Title: "Conflict", Status: http.StatusConflict, Detail: "secrets are read-only: manage them in the external secret store"}, true
	case errors.Is(err, secrets.ErrUnavailable):
		return Problem{Type: typeUnavailable, Title: "Service Unavailable", Status: http.StatusServiceUnavailable, Detail: "the secret backend rejected the request"}, true
	case errors.Is(err, secrets.ErrDisabled):
		return Problem{Type: typeUnavailable, Title: "Service Unavailable", Status: http.StatusServiceUnavailable, Detail: "secrets are disabled: no KEK is configured"}, true
	}
	return Problem{}, false
}
