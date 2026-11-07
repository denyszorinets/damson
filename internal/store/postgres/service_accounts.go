package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

var _ store.ServiceAccountStore = (*Store)(nil)

func toServiceAccount(r gen.ServiceAccount) store.ServiceAccount {
	return store.ServiceAccount{ID: r.ID, Name: r.Name, Description: r.Description, CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC()}
}

func toServiceToken(r gen.ServiceToken) store.ServiceToken {
	return store.ServiceToken{
		ID: r.ID, ServiceAccountID: r.ServiceAccountID, Name: r.Name, Project: r.Project, Role: r.Role,
		ExpiresAt: tsPtr(r.ExpiresAt), RevokedAt: tsPtr(r.RevokedAt), LastUsedAt: tsPtr(r.LastUsedAt), CreatedAt: r.CreatedAt.Time.UTC(),
	}
}

// CreateServiceAccount implements store.ServiceAccountStore.
func (s *Store) CreateServiceAccount(ctx context.Context, a store.ServiceAccount, now time.Time) (store.ServiceAccount, error) {
	if a.ID == "" || a.Name == "" {
		return store.ServiceAccount{}, fmt.Errorf("a service account needs an id and a name: %w", store.ErrInvalid)
	}
	ev, err := store.NewServiceEvent(ctx, store.ActionServiceAccountCreate, store.AuditTargetServiceAccount, a.ID, "", store.ServiceAuditDetails{Name: a.Name}, now)
	if err != nil {
		return store.ServiceAccount{}, err
	}
	err = s.WithTx(ctx, func(tx store.Tx) error {
		err := tx.(*txn).q.InsertServiceAccount(ctx, gen.InsertServiceAccountParams{ID: a.ID, Name: a.Name, Description: a.Description, CreatedAt: ts(now)})
		if isUniqueViolation(err) {
			return fmt.Errorf("service account %q: %w", a.Name, store.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("creating service account: %w", err)
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
	if err != nil {
		return store.ServiceAccount{}, err
	}
	a.CreatedAt, a.UpdatedAt = store.NormalizeTime(now), store.NormalizeTime(now)
	return a, nil
}

// GetServiceAccount implements store.ServiceAccountStore.
func (s *Store) GetServiceAccount(ctx context.Context, id string) (store.ServiceAccount, error) {
	r, err := s.q.GetServiceAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ServiceAccount{}, fmt.Errorf("service account %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.ServiceAccount{}, fmt.Errorf("reading service account: %w", err)
	}
	return toServiceAccount(r), nil
}

// ListServiceAccounts implements store.ServiceAccountStore.
func (s *Store) ListServiceAccounts(ctx context.Context, afterID string, limit int) ([]store.ServiceAccount, error) {
	rows, err := s.q.ListServiceAccounts(ctx, gen.ListServiceAccountsParams{AfterID: afterID, RowLimit: int32(store.ClampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("listing service accounts: %w", err)
	}
	out := make([]store.ServiceAccount, len(rows))
	for i, r := range rows {
		out[i] = toServiceAccount(r)
	}
	return out, nil
}

// DeleteServiceAccount implements store.ServiceAccountStore.
func (s *Store) DeleteServiceAccount(ctx context.Context, id string, now time.Time) error {
	if _, err := store.AuditFrom(ctx); err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		cur, err := q.GetServiceAccount(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("service account %q: %w", id, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading service account: %w", err)
		}
		if _, err := q.DeleteServiceAccount(ctx, id); err != nil {
			return fmt.Errorf("deleting service account: %w", err)
		}
		ev, err := store.NewServiceEvent(ctx, store.ActionServiceAccountDelete, store.AuditTargetServiceAccount, id, "", store.ServiceAuditDetails{Name: cur.Name}, now)
		if err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
}

// CreateServiceToken implements store.ServiceAccountStore.
func (s *Store) CreateServiceToken(ctx context.Context, n store.NewServiceToken, now time.Time) (store.ServiceToken, error) {
	if err := n.Validate(); err != nil {
		return store.ServiceToken{}, err
	}
	d := store.ServiceAuditDetails{ServiceAccountID: n.ServiceAccountID, Name: n.Name, Project: n.Project, Role: n.Role}
	if n.ExpiresAt != nil {
		d.ExpiresAt = n.ExpiresAt.UTC().Format(time.RFC3339)
	}
	ev, err := store.NewServiceEvent(ctx, store.ActionServiceTokenCreate, store.AuditTargetServiceToken, n.ID, n.Project, d, now)
	if err != nil {
		return store.ServiceToken{}, err
	}
	err = s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		if _, err := q.GetServiceAccount(ctx, n.ServiceAccountID); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("service account %q: %w", n.ServiceAccountID, store.ErrNotFound)
		} else if err != nil {
			return fmt.Errorf("reading service account: %w", err)
		}
		if err := q.InsertServiceToken(ctx, gen.InsertServiceTokenParams{
			ID: n.ID, ServiceAccountID: n.ServiceAccountID, Name: n.Name, TokenHash: n.TokenHash, Project: n.Project, Role: n.Role,
			ExpiresAt: tsOpt(n.ExpiresAt), CreatedAt: ts(now),
		}); err != nil {
			return fmt.Errorf("creating service token: %w", err)
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
	if err != nil {
		return store.ServiceToken{}, err
	}
	out := n.ServiceToken
	out.CreatedAt = store.NormalizeTime(now)
	return out, nil
}

// GetServiceToken implements store.ServiceAccountStore.
func (s *Store) GetServiceToken(ctx context.Context, id string) (store.ServiceToken, error) {
	r, err := s.q.GetServiceToken(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ServiceToken{}, fmt.Errorf("service token %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.ServiceToken{}, fmt.Errorf("reading service token: %w", err)
	}
	return toServiceToken(r), nil
}

// ListServiceTokens implements store.ServiceAccountStore.
func (s *Store) ListServiceTokens(ctx context.Context, accountID string) ([]store.ServiceToken, error) {
	rows, err := s.q.ListServiceTokens(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("listing service tokens: %w", err)
	}
	out := make([]store.ServiceToken, len(rows))
	for i, r := range rows {
		out[i] = toServiceToken(r)
	}
	return out, nil
}

// RevokeServiceToken implements store.ServiceAccountStore.
func (s *Store) RevokeServiceToken(ctx context.Context, id string, now time.Time) error {
	if _, err := store.AuditFrom(ctx); err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		cur, err := q.GetServiceToken(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("service token %q: %w", id, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading service token: %w", err)
		}
		n, err := q.RevokeServiceToken(ctx, gen.RevokeServiceTokenParams{RevokedAt: ts(now), ID: id})
		if err != nil {
			return fmt.Errorf("revoking service token: %w", err)
		}
		if n == 0 {
			return nil // already revoked
		}
		ev, err := store.NewServiceEvent(ctx, store.ActionServiceTokenRevoke, store.AuditTargetServiceToken, id, cur.Project,
			store.ServiceAuditDetails{ServiceAccountID: cur.ServiceAccountID, Name: cur.Name, Project: cur.Project, Role: cur.Role}, now)
		if err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
}

// FindServiceToken implements store.ServiceAccountStore.
func (s *Store) FindServiceToken(ctx context.Context, tokenHash string, now time.Time) (store.ServiceToken, error) {
	r, err := s.q.FindActiveServiceToken(ctx, gen.FindActiveServiceTokenParams{TokenHash: tokenHash, Now: ts(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ServiceToken{}, store.ErrNotFound // no hash in the message
	}
	if err != nil {
		return store.ServiceToken{}, fmt.Errorf("finding service token: %w", err)
	}
	return toServiceToken(r), nil
}

// TouchServiceToken implements store.ServiceAccountStore.
func (s *Store) TouchServiceToken(ctx context.Context, id string, now time.Time) error {
	if _, err := s.q.TouchServiceToken(ctx, gen.TouchServiceTokenParams{Now: ts(now), ID: id, OlderThan: ts(now.Add(-store.ServiceTokenTouchWindow))}); err != nil {
		return fmt.Errorf("touching service token: %w", err)
	}
	return nil
}
