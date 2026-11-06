package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

var _ store.SecretStore = (*Store)(nil)

func toSecretMeta(id, scope, name, typ string, cur int64, created, updated, fields string) (store.SecretMeta, error) {
	c, err := parseTime(created)
	if err != nil {
		return store.SecretMeta{}, err
	}
	u, err := parseTime(updated)
	if err != nil {
		return store.SecretMeta{}, err
	}
	names, err := store.DecodeFieldNames(fields)
	if err != nil {
		return store.SecretMeta{}, err
	}
	return store.SecretMeta{ID: id, Scope: scope, Name: name, Type: typ, CurrentVersion: cur, FieldNames: names, CreatedAt: c, UpdatedAt: u}, nil
}

func versionParams(id string, version int64, v store.SecretBlob, now time.Time) (gen.InsertSecretVersionParams, error) {
	fields, err := store.EncodeFieldNames(v.FieldNames)
	if err != nil {
		return gen.InsertSecretVersionParams{}, err
	}
	return gen.InsertSecretVersionParams{
		SecretID: id, Version: version, KekID: v.KEKID, WrappedDek: v.WrappedDEK, Nonce: v.Nonce,
		Ciphertext: v.Ciphertext, FieldNames: fields, CreatedAt: fmtTime(now),
	}, nil
}

// CreateSecret implements store.SecretStore.
func (s *Store) CreateSecret(ctx context.Context, id, scope, name, typ string, v store.SecretBlob, now time.Time) (store.SecretMeta, error) {
	vp, err := versionParams(id, 1, v, now)
	if err != nil {
		return store.SecretMeta{}, err
	}
	err = s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		err := q.InsertSecret(ctx, gen.InsertSecretParams{ID: id, ScopePath: scope, Name: name, Type: typ, CreatedAt: fmtTime(now), UpdatedAt: fmtTime(now)})
		if isUnique(err) {
			return fmt.Errorf("secret %s/%s: %w", scope, name, store.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("creating secret: %w", err)
		}
		if err := q.InsertSecretVersion(ctx, vp); err != nil {
			return fmt.Errorf("creating secret version: %w", err)
		}
		return nil
	})
	if err != nil {
		return store.SecretMeta{}, err
	}
	n := store.NormalizeTime(now)
	return store.SecretMeta{ID: id, Scope: scope, Name: name, Type: typ, CurrentVersion: 1, FieldNames: v.FieldNames, CreatedAt: n, UpdatedAt: n}, nil
}

// AddSecretVersion implements store.SecretStore.
func (s *Store) AddSecretVersion(ctx context.Context, id string, expected int64, v store.SecretBlob, now time.Time) (store.SecretMeta, error) {
	var meta store.SecretMeta
	err := s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		n, err := q.BumpSecretVersionCAS(ctx, gen.BumpSecretVersionCASParams{UpdatedAt: fmtTime(now), ID: id, ExpectedVersion: expected})
		if err != nil {
			return fmt.Errorf("bumping secret version: %w", err)
		}
		head, herr := q.GetSecretHeadByID(ctx, id)
		if errors.Is(herr, sql.ErrNoRows) {
			return fmt.Errorf("secret %s: %w", id, store.ErrNotFound)
		}
		if herr != nil {
			return fmt.Errorf("reading secret: %w", herr)
		}
		if n == 0 {
			return fmt.Errorf("secret %s at version %d: %w", id, expected, store.ErrStale)
		}
		vp, err := versionParams(id, expected+1, v, now)
		if err != nil {
			return err
		}
		if err := q.InsertSecretVersion(ctx, vp); err != nil {
			return fmt.Errorf("adding secret version: %w", err)
		}
		if err := q.PruneSecretVersions(ctx, gen.PruneSecretVersionsParams{SecretID: id, MaxPruned: expected + 1 - store.SecretVersionsKept}); err != nil {
			return fmt.Errorf("pruning secret versions: %w", err)
		}
		meta, err = toSecretMeta(head.ID, head.ScopePath, head.Name, head.Type, expected+1, head.CreatedAt, head.UpdatedAt, "[]")
		meta.FieldNames = v.FieldNames
		return err
	})
	return meta, err
}

// GetSecret implements store.SecretStore.
func (s *Store) GetSecret(ctx context.Context, scope, name string) (store.SecretMeta, store.SecretBlob, error) {
	r, err := s.q.GetSecretCurrent(ctx, gen.GetSecretCurrentParams{ScopePath: scope, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return store.SecretMeta{}, store.SecretBlob{}, fmt.Errorf("secret %s/%s: %w", scope, name, store.ErrNotFound)
	}
	if err != nil {
		return store.SecretMeta{}, store.SecretBlob{}, fmt.Errorf("getting secret: %w", err)
	}
	m, err := toSecretMeta(r.ID, r.ScopePath, r.Name, r.Type, r.CurrentVersion, r.CreatedAt, r.UpdatedAt, r.FieldNames)
	if err != nil {
		return store.SecretMeta{}, store.SecretBlob{}, err
	}
	return m, store.SecretBlob{KEKID: r.KekID, WrappedDEK: r.WrappedDek, Nonce: r.Nonce, Ciphertext: r.Ciphertext, FieldNames: m.FieldNames}, nil
}

// GetSecretMeta implements store.SecretStore.
func (s *Store) GetSecretMeta(ctx context.Context, scope, name string) (store.SecretMeta, error) {
	m, _, err := s.GetSecret(ctx, scope, name)
	return m, err
}

// ListSecrets implements store.SecretStore.
func (s *Store) ListSecrets(ctx context.Context, scope, afterScope, afterName string, limit int) ([]store.SecretMeta, error) {
	rows, err := s.q.ListSecretMeta(ctx, gen.ListSecretMetaParams{ScopeFilter: scope, AfterScope: afterScope, AfterName: afterName, RowLimit: int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("listing secrets: %w", err)
	}
	out := make([]store.SecretMeta, 0, len(rows))
	for _, r := range rows {
		m, err := toSecretMeta(r.ID, r.ScopePath, r.Name, r.Type, r.CurrentVersion, r.CreatedAt, r.UpdatedAt, r.FieldNames)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// DeleteSecret implements store.SecretStore.
func (s *Store) DeleteSecret(ctx context.Context, scope, name string) error {
	n, err := s.q.DeleteSecretByName(ctx, gen.DeleteSecretByNameParams{ScopePath: scope, Name: name})
	if err != nil {
		return fmt.Errorf("deleting secret: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("secret %s/%s: %w", scope, name, store.ErrNotFound)
	}
	return nil
}

// ListSecretVersionsNotUnder implements store.SecretStore.
func (s *Store) ListSecretVersionsNotUnder(ctx context.Context, kekID, afterSecretID string, afterVersion int64, limit int) ([]store.WrappedVersion, error) {
	rows, err := s.q.ListSecretVersionsNotUnder(ctx, gen.ListSecretVersionsNotUnderParams{
		KekID: kekID, AfterSecret: afterSecretID, AfterVersion: afterVersion, RowLimit: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("listing secret versions to rotate: %w", err)
	}
	out := make([]store.WrappedVersion, len(rows))
	for i, r := range rows {
		out[i] = store.WrappedVersion{SecretID: r.SecretID, Version: int64(r.Version), Scope: r.ScopePath, Name: r.Name, KEKID: r.KekID, WrappedDEK: r.WrappedDek}
	}
	return out, nil
}

// RewrapSecretVersion implements store.SecretStore.
func (s *Store) RewrapSecretVersion(ctx context.Context, secretID string, version int64, oldKEKID, newKEKID string, wrappedDEK []byte) (bool, error) {
	n, err := s.q.RewrapSecretVersionCAS(ctx, gen.RewrapSecretVersionCASParams{
		NewKekID: newKEKID, WrappedDek: wrappedDEK, SecretID: secretID, Version: version, OldKekID: oldKEKID,
	})
	if err != nil {
		return false, fmt.Errorf("rewrapping secret version: %w", err)
	}
	return n > 0, nil
}
