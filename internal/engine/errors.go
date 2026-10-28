package engine

import (
	"fmt"
	"strings"
)

// FieldError is one validation problem at a precise location in the spec.
type FieldError struct {
	// Path locates the problem, e.g. "spec.steps[1].id".
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Path + ": " + e.Message }

// ValidationErrors is the error returned for an invalid spec. It lists every
// problem found, in document order.
type ValidationErrors []FieldError

func (v ValidationErrors) Error() string {
	parts := make([]string, len(v))
	for i, e := range v {
		parts[i] = e.Error()
	}
	return "invalid job spec: " + strings.Join(parts, "; ")
}

// errNotSupported is the message for fields reserved for a later milestone.
func errNotSupported(path, what string) FieldError {
	return FieldError{Path: path, Message: fmt.Sprintf("%s is not supported yet", what)}
}
