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

var _ store.SessionStore = (*Store)(nil)

func toSession(r gen.Session) store.Session {
	return store.Session{
		ID: r.ID, Sub: r.Sub, Email: r.Email, Name: r.Name, Claims: []byte(r.Claims), InstanceAdmin: r.InstanceAdmin,
		CreatedAt: r.CreatedAt.Time.UTC(), LastSeenAt: r.LastSeenAt.Time.UTC(), ExpiresAt: r.ExpiresAt.Time.UTC(),
	}
}

// CreateOIDCLogin implements store.SessionStore.
func (s *Store) CreateOIDCLogin(ctx context.Context, l store.OIDCLogin) error {
	if len(l.StateHash) != 64 || len(l.BindingHash) != 64 || l.Nonce == "" || l.Verifier == "" {
		return fmt.Errorf("a login needs hashed state and binding, a nonce and a verifier: %w", store.ErrInvalid)
	}
	err := s.q.InsertOIDCLogin(ctx, gen.InsertOIDCLoginParams{
		StateHash: l.StateHash, BindingHash: l.BindingHash, Nonce: l.Nonce, Verifier: l.Verifier, ReturnTo: l.ReturnTo, ExpiresAt: ts(l.ExpiresAt),
	})
	if isUniqueViolation(err) {
		return fmt.Errorf("login state: %w", store.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("creating login: %w", err)
	}
	return nil
}

// ConsumeOIDCLogin implements store.SessionStore.
func (s *Store) ConsumeOIDCLogin(ctx context.Context, stateHash string, now time.Time) (store.OIDCLogin, error) {
	r, err := s.q.ConsumeOIDCLogin(ctx, gen.ConsumeOIDCLoginParams{StateHash: stateHash, Now: ts(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.OIDCLogin{}, fmt.Errorf("login: %w", store.ErrNotFound)
	}
	if err != nil {
		return store.OIDCLogin{}, fmt.Errorf("consuming login: %w", err)
	}
	return store.OIDCLogin{
		StateHash: r.StateHash, BindingHash: r.BindingHash, Nonce: r.Nonce, Verifier: r.Verifier, ReturnTo: r.ReturnTo, ExpiresAt: r.ExpiresAt.Time.UTC(),
	}, nil
}

// CreateSession implements store.SessionStore.
func (s *Store) CreateSession(ctx context.Context, n store.NewSession, now time.Time) error {
	if err := n.Validate(); err != nil {
		return err
	}
	err := s.WithTx(ctx, func(tx store.Tx) error {
		err := tx.(*txn).q.InsertSession(ctx, gen.InsertSessionParams{
			ID: n.ID, TokenHash: n.TokenHash, Sub: n.Sub, Email: n.Email, Name: n.Name, Claims: string(n.Claims),
			InstanceAdmin: n.InstanceAdmin, CreatedAt: ts(now), ExpiresAt: ts(n.ExpiresAt),
		})
		if isUniqueViolation(err) {
			return fmt.Errorf("session: %w", store.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
		if n.Audit != nil {
			return tx.AppendAuditEvent(ctx, *n.Audit)
		}
		return nil
	})
	return err
}

// FindSession implements store.SessionStore.
func (s *Store) FindSession(ctx context.Context, tokenHash string, now time.Time, idle time.Duration) (store.Session, error) {
	r, err := s.q.FindSession(ctx, gen.FindSessionParams{TokenHash: tokenHash, Now: ts(now), IdleCutoff: ts(now.Add(-idle))})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Session{}, fmt.Errorf("session: %w", store.ErrNotFound)
	}
	if err != nil {
		return store.Session{}, fmt.Errorf("finding session: %w", err)
	}
	return toSession(r), nil
}

// TouchSession implements store.SessionStore.
func (s *Store) TouchSession(ctx context.Context, id string, now time.Time) error {
	if err := s.q.TouchSession(ctx, gen.TouchSessionParams{ID: id, Now: ts(now)}); err != nil {
		return fmt.Errorf("touching session: %w", err)
	}
	return nil
}

// DeleteSession implements store.SessionStore.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := s.q.DeleteSessionByHash(ctx, tokenHash); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

// PurgeExpiredAuth implements store.SessionStore.
func (s *Store) PurgeExpiredAuth(ctx context.Context, now time.Time, idle time.Duration) (int64, int64, error) {
	ns, err := s.q.PurgeSessions(ctx, gen.PurgeSessionsParams{Now: ts(now), IdleCutoff: ts(now.Add(-idle))})
	if err != nil {
		return 0, 0, fmt.Errorf("purging sessions: %w", err)
	}
	nl, err := s.q.PurgeOIDCLogins(ctx, ts(now))
	if err != nil {
		return ns, 0, fmt.Errorf("purging logins: %w", err)
	}
	return ns, nl, nil
}
