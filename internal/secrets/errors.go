package secrets

import "errors"

// Errors of a secret backend. Messages never contain secret values.
var (
	// ErrNotFound means no secret has that scope and name.
	ErrNotFound = errors.New("secret not found")
	// ErrVersionConflict means the secret changed since the caller read it,
	// or it exists although the caller asked to create it.
	ErrVersionConflict = errors.New("secret version conflict")
	// ErrTypeChange means a new version names a different type than the secret has.
	ErrTypeChange = errors.New("secret type cannot change")
	// ErrDisabled means no KEK is configured, so secrets are unavailable.
	ErrDisabled = errors.New("secrets are disabled: no KEK configured")
)

// Errors of an external secret backend (ADR-0068).
var (
	// ErrReadOnly means the backend is configured read-only: secrets are
	// managed in the external secret store.
	ErrReadOnly = errors.New("secrets are read-only: manage them in the external secret store")
	// ErrUnavailable means a secret exists but cannot be used: the backend
	// rejected the controller's credentials or holds a malformed value. A
	// retry will not help.
	ErrUnavailable = errors.New("secret unavailable")
)
