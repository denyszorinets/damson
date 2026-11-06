package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SecretVersionsKept is how many versions of a secret are retained.
const SecretVersionsKept = 10

// SecretMeta describes a secret without any encrypted material.
type SecretMeta struct {
	ID             string
	Scope          string
	Name           string
	Type           string
	CurrentVersion int64
	FieldNames     []string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SecretBlob is the encrypted form of one version, opaque to the store.
type SecretBlob struct {
	KEKID      string
	WrappedDEK []byte
	Nonce      []byte
	Ciphertext []byte
	FieldNames []string
}

// SecretStore persists envelope-encrypted secrets (ARCHITECTURE 9). The
// store never sees plaintext; encryption is the caller's job.
type SecretStore interface {
	// CreateSecret inserts version 1 and returns ErrConflict if scope/name exists.
	CreateSecret(ctx context.Context, id, scope, name, typ string, v SecretBlob, now time.Time) (SecretMeta, error)
	// AddSecretVersion adds version expected+1 to the secret with this id (CAS
	// on its current version, so a delete and recreate under the same name
	// cannot be mistaken for it), prunes old versions and returns ErrNotFound
	// or ErrStale. The metadata describes the write, not a later read.
	AddSecretVersion(ctx context.Context, id string, expected int64, v SecretBlob, now time.Time) (SecretMeta, error)
	// GetSecret returns the metadata and encrypted current version, or ErrNotFound.
	GetSecret(ctx context.Context, scope, name string) (SecretMeta, SecretBlob, error)
	// GetSecretMeta returns the metadata only, or ErrNotFound.
	GetSecretMeta(ctx context.Context, scope, name string) (SecretMeta, error)
	// ListSecrets pages by (scope, name). An empty scope lists all scopes;
	// afterScope/afterName is the keyset cursor. It never returns encrypted bytes.
	ListSecrets(ctx context.Context, scope, afterScope, afterName string, limit int) ([]SecretMeta, error)
	// ListSecretVersionsNotUnder pages (by secret id, version) through the
	// versions whose DEK is not wrapped under kekID, for rotate-kek. It never
	// returns the data ciphertext.
	ListSecretVersionsNotUnder(ctx context.Context, kekID, afterSecretID string, afterVersion int64, limit int) ([]WrappedVersion, error)
	// RewrapSecretVersion replaces the wrapped DEK and kek_id of one version if
	// it is still wrapped under oldKEKID, and reports whether it did (false:
	// already rotated, or the secret was deleted).
	RewrapSecretVersion(ctx context.Context, secretID string, version int64, oldKEKID, newKEKID string, wrappedDEK []byte) (bool, error)
	// DeleteSecret removes the secret and all its versions, or returns ErrNotFound.
	// CreateSecret, AddSecretVersion and DeleteSecret write an audit event in
	// the same transaction for the actor in ctx (WithAudit) and fail with
	// ErrInvalid without one.
	DeleteSecret(ctx context.Context, scope, name string, now time.Time) error
}

// EncodeFieldNames renders field names as the JSON array stored with a version.
func EncodeFieldNames(names []string) (string, error) {
	if names == nil {
		names = []string{}
	}
	b, err := json.Marshal(names)
	return string(b), err
}

// DecodeFieldNames is the inverse of EncodeFieldNames.
func DecodeFieldNames(s string) ([]string, error) {
	var names []string
	if err := json.Unmarshal([]byte(s), &names); err != nil {
		return nil, fmt.Errorf("decoding secret field names: %w", err)
	}
	return names, nil
}

// WrappedVersion is the key-wrapping part of one secret version.
type WrappedVersion struct {
	SecretID   string
	Version    int64
	Scope      string
	Name       string
	KEKID      string
	WrappedDEK []byte
}
