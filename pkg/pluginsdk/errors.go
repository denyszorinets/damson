package pluginsdk

import (
	"context"
	"errors"
	"fmt"
)

// ErrorKind classifies a step failure. The engine's retry rules key off it.
type ErrorKind string

// Error kinds.
const (
	KindConnection ErrorKind = "connection"
	KindAuth       ErrorKind = "auth"
	KindTimeout    ErrorKind = "timeout"
	KindExitCode   ErrorKind = "exit_code"
	KindValidation ErrorKind = "validation"
	KindCancelled  ErrorKind = "cancelled"
	KindInternal   ErrorKind = "internal"
)

// Valid reports whether k is a known kind.
func (k ErrorKind) Valid() bool {
	switch k {
	case KindConnection, KindAuth, KindTimeout, KindExitCode, KindValidation, KindCancelled, KindInternal:
		return true
	}
	return false
}

// StepError is a typed step failure. It implements error.
type StepError struct {
	Kind    ErrorKind `json:"kind"`
	Message string    `json:"message"`
	// ExitCode is set for KindExitCode.
	ExitCode *int `json:"exit_code,omitempty"`
	cause    error
}

// Error implements error.
func (e *StepError) Error() string { return fmt.Sprintf("%s: %s", e.Kind, e.Message) }

// Unwrap returns the underlying cause, if any (never serialized).
func (e *StepError) Unwrap() error { return e.cause }

// NewError builds a StepError. cause may be nil. Messages must not contain
// secret values.
func NewError(kind ErrorKind, msg string, cause error) *StepError {
	return &StepError{Kind: kind, Message: msg, cause: cause}
}

// ValidationError is a non-retryable input validation failure.
func ValidationError(format string, args ...any) *StepError {
	return NewError(KindValidation, fmt.Sprintf(format, args...), nil)
}

// ExitCodeError reports a non-zero process exit code.
func ExitCodeError(code int, msg string) *StepError {
	e := NewError(KindExitCode, msg, nil)
	e.ExitCode = &code
	return e
}

// AsStepError normalizes any error into a StepError. A nil error returns nil.
// Context errors map to cancelled/timeout; unknown errors map to internal.
func AsStepError(err error) *StepError {
	if err == nil {
		return nil
	}
	var se *StepError
	switch {
	case errors.As(err, &se):
		return se
	case errors.Is(err, context.Canceled):
		return NewError(KindCancelled, "cancelled", err)
	case errors.Is(err, context.DeadlineExceeded):
		return NewError(KindTimeout, "deadline exceeded", err)
	}
	return NewError(KindInternal, err.Error(), err)
}
