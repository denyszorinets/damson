// Package db is the secret backend that keeps envelope-encrypted secrets in
// the store (ARCHITECTURE 9.2).
package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store"
)

// maxAttempts bounds retries of an unconditional write that races another.
const maxAttempts = 3

// Backend reads and writes secrets through a store.SecretStore.
type Backend struct {
	st    store.SecretStore
	ring  *secrets.Keyring
	rand  io.Reader
	now   func() time.Time
	newID func() string
}

// New returns a Backend. rand feeds the encryption, newID names new secrets.
func New(st store.SecretStore, ring *secrets.Keyring, rand io.Reader, now func() time.Time, newID func() string) *Backend {
	return &Backend{st: st, ring: ring, rand: rand, now: now, newID: newID}
}

// Put creates a secret or adds a version. ifVersion nil writes whatever the
// current state is; 0 requires that the secret does not exist; n requires
// current version n. created reports whether the secret is new.
func (b *Backend) Put(ctx context.Context, scope, name, typ string, fields map[string]string, ifVersion *int64) (meta store.SecretMeta, created bool, err error) {
	if err := secrets.ValidateScope(scope); err != nil {
		return store.SecretMeta{}, false, err
	}
	if err := secrets.ValidateName(name); err != nil {
		return store.SecretMeta{}, false, err
	}
	t, err := secrets.LookupType(typ)
	if err != nil {
		return store.SecretMeta{}, false, err
	}
	if err := t.ValidateFields(fields); err != nil {
		return store.SecretMeta{}, false, err
	}
	for range maxAttempts {
		meta, created, err = b.putOnce(ctx, scope, name, typ, fields, ifVersion)
		if !errors.Is(err, errRetry) {
			return meta, created, err
		}
	}
	return store.SecretMeta{}, false, fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrVersionConflict)
}

var errRetry = errors.New("retry")

func (b *Backend) putOnce(ctx context.Context, scope, name, typ string, fields map[string]string, ifVersion *int64) (store.SecretMeta, bool, error) {
	cur, err := b.st.GetSecretMeta(ctx, scope, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if ifVersion != nil && *ifVersion != 0 {
			return store.SecretMeta{}, false, fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrNotFound)
		}
		return b.create(ctx, scope, name, typ, fields, ifVersion != nil)
	case err != nil:
		return store.SecretMeta{}, false, err
	}
	if ifVersion != nil && *ifVersion != cur.CurrentVersion {
		return store.SecretMeta{}, false, fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrVersionConflict)
	}
	if cur.Type != typ {
		return store.SecretMeta{}, false, fmt.Errorf("secret %s/%s has type %s: %w", scope, name, cur.Type, secrets.ErrTypeChange)
	}
	blob, err := b.seal(scope, name, cur.CurrentVersion+1, fields)
	if err != nil {
		return store.SecretMeta{}, false, err
	}
	m, err := b.st.AddSecretVersion(ctx, cur.ID, cur.CurrentVersion, blob, b.now())
	if errors.Is(err, store.ErrStale) || errors.Is(err, store.ErrNotFound) {
		return store.SecretMeta{}, false, b.raced(scope, name, ifVersion != nil)
	}
	return m, false, err
}

func (b *Backend) create(ctx context.Context, scope, name, typ string, fields map[string]string, strict bool) (store.SecretMeta, bool, error) {
	blob, err := b.seal(scope, name, 1, fields)
	if err != nil {
		return store.SecretMeta{}, false, err
	}
	m, err := b.st.CreateSecret(ctx, b.newID(), scope, name, typ, blob, b.now())
	if errors.Is(err, store.ErrConflict) {
		return store.SecretMeta{}, false, b.raced(scope, name, strict)
	}
	return m, err == nil, err
}

// raced is the error of a write that lost a race: a conditional write fails,
// an unconditional one is retried.
func (b *Backend) raced(scope, name string, strict bool) error {
	if strict {
		return fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrVersionConflict)
	}
	return errRetry
}

func (b *Backend) seal(scope, name string, version int64, fields map[string]string) (store.SecretBlob, error) {
	s, err := b.ring.Seal(b.rand, scope, name, version, fields)
	if err != nil {
		return store.SecretBlob{}, fmt.Errorf("encrypting secret %s/%s: %w", scope, name, err)
	}
	names := slices.Sorted(maps.Keys(fields))
	return store.SecretBlob{KEKID: s.KEKID, WrappedDEK: s.WrappedDEK, Nonce: s.Nonce, Ciphertext: s.Ciphertext, FieldNames: names}, nil
}

// Get decrypts the current version of scope/name. The caller owns the
// returned values and should clear the map when done.
func (b *Backend) Get(ctx context.Context, scope, name string) (map[string]string, error) {
	meta, blob, err := b.st.GetSecret(ctx, scope, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	fields, err := b.ring.Open(scope, name, meta.CurrentVersion, secrets.Sealed{
		KEKID: blob.KEKID, WrappedDEK: blob.WrappedDEK, Nonce: blob.Nonce, Ciphertext: blob.Ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("secret %s/%s kek %s: %w", scope, name, blob.KEKID, err)
	}
	return fields, nil
}

// List returns metadata, never values. An empty scope lists every scope.
func (b *Backend) List(ctx context.Context, scope, afterScope, afterName string, limit int) ([]store.SecretMeta, error) {
	return b.st.ListSecrets(ctx, scope, afterScope, afterName, limit)
}

// Meta returns the metadata of one secret.
func (b *Backend) Meta(ctx context.Context, scope, name string) (store.SecretMeta, error) {
	m, err := b.st.GetSecretMeta(ctx, scope, name)
	if errors.Is(err, store.ErrNotFound) {
		return m, fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrNotFound)
	}
	return m, err
}

// Delete removes a secret and all its versions.
func (b *Backend) Delete(ctx context.Context, scope, name string) error {
	err := b.st.DeleteSecret(ctx, scope, name, b.now())
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("secret %s/%s: %w", scope, name, secrets.ErrNotFound)
	}
	return err
}
