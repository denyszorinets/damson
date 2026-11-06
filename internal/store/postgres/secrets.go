package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

var _ store.SecretStore = (*Store)(nil)

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == uniqueViolation
}

func toSecretMeta(id, scope, name, typ string, cur int64, c, u pgtype.Timestamptz, fields string) (store.SecretMeta, error) {
	names, err := store.DecodeFieldNames(fields)
	if err != nil {
		return store.SecretMeta{}, err
	}
	return store.SecretMeta{ID: id, Scope: scope, Name: name, Type: typ, CurrentVersion: cur, FieldNames: names, CreatedAt: c.Time.UTC(), UpdatedAt: u.Time.UTC()}, nil
}

func versionParams(id string, version int64, v store.SecretBlob, now time.Time) (gen.InsertSecretVersionParams, error) {
	fields, err := store.EncodeFieldNames(v.FieldNames)
	if err != nil {
		return gen.InsertSecretVersionParams{}, err
	}
	return gen.InsertSecretVersionParams{
		SecretID: id, Version: version, KekID: v.KEKID, WrappedDek: v.WrappedDEK, Nonce: v.Nonce,
		Ciphertext: v.Ciphertext, FieldNames: fields, CreatedAt: ts(now),
	}, nil
}

// CreateSecret implements store.SecretStore.
func (s *Store) CreateSecret(ctx context.Context, id, scope, name, typ string, v store.SecretBlob, now time.Time) (store.SecretMeta, error) {
	vp, err := versionParams(id, 1, v, now)
	if err != nil {
		return store.SecretMeta{}, err
	}
	ev, err := store.NewSecretEvent(ctx, store.ActionSecretCreate, scope, name,
		store.SecretAuditDetails{SecretID: id, Type: typ, Version: 1, Fields: v.FieldNames, KEKID: v.KEKID}, now)
	if err != nil {
		return store.SecretMeta{}, err
	}
	err = s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		err := q.InsertSecret(ctx, gen.InsertSecretParams{ID: id, ScopePath: scope, Name: name, Type: typ, CreatedAt: ts(now), UpdatedAt: ts(now)})
		if isUniqueViolation(err) {
			return fmt.Errorf("secret %s/%s: %w", scope, name, store.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("creating secret: %w", err)
		}
		if err := q.InsertSecretVersion(ctx, vp); err != nil {
			return fmt.Errorf("creating secret version: %w", err)
		}
		return tx.AppendAuditEvent(ctx, ev)
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
	if _, err := store.AuditFrom(ctx); err != nil {
		return meta, err
	}
	err := s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		n, err := q.BumpSecretVersionCAS(ctx, gen.BumpSecretVersionCASParams{UpdatedAt: ts(now), ID: id, ExpectedVersion: expected})
		if err != nil {
			return fmt.Errorf("bumping secret version: %w", err)
		}
		head, herr := q.GetSecretHeadByID(ctx, id)
		if errors.Is(herr, pgx.ErrNoRows) {
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
		if err != nil {
			return err
		}
		meta.FieldNames = v.FieldNames
		ev, err := store.NewSecretEvent(ctx, store.ActionSecretUpdate, head.ScopePath, head.Name,
			store.SecretAuditDetails{SecretID: id, Type: head.Type, Version: expected + 1, Fields: v.FieldNames, KEKID: v.KEKID}, now)
		if err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
	return meta, err
}

// GetSecret implements store.SecretStore.
func (s *Store) GetSecret(ctx context.Context, scope, name string) (store.SecretMeta, store.SecretBlob, error) {
	r, err := s.q.GetSecretCurrent(ctx, gen.GetSecretCurrentParams{ScopePath: scope, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
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
	rows, err := s.q.ListSecretMeta(ctx, gen.ListSecretMetaParams{ScopeFilter: scope, AfterScope: afterScope, AfterName: afterName, RowLimit: int32(limit)})
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
func (s *Store) DeleteSecret(ctx context.Context, scope, name string, now time.Time) error {
	if _, err := store.AuditFrom(ctx); err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx store.Tx) error {
		row, err := tx.(*txn).q.DeleteSecretByName(ctx, gen.DeleteSecretByNameParams{ScopePath: scope, Name: name})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("secret %s/%s: %w", scope, name, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("deleting secret: %w", err)
		}
		ev, err := store.NewSecretEvent(ctx, store.ActionSecretDelete, scope, name,
			store.SecretAuditDetails{SecretID: row.ID, Type: row.Type, Version: int64(row.CurrentVersion)}, now)
		if err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
}

// ListSecretVersionsNotUnder implements store.SecretStore.
func (s *Store) ListSecretVersionsNotUnder(ctx context.Context, kekID, afterSecretID string, afterVersion int64, limit int) ([]store.WrappedVersion, error) {
	rows, err := s.q.ListSecretVersionsNotUnder(ctx, gen.ListSecretVersionsNotUnderParams{
		KekID: kekID, AfterSecret: afterSecretID, AfterVersion: afterVersion, RowLimit: int32(limit),
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
