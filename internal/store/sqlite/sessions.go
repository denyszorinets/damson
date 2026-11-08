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

var _ store.SessionStore = (*Store)(nil)

func toSession(r gen.Session) (store.Session, error) {
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return store.Session{}, err
	}
	seen, err := parseTime(r.LastSeenAt)
	if err != nil {
		return store.Session{}, err
	}
	exp, err := parseTime(r.ExpiresAt)
	if err != nil {
		return store.Session{}, err
	}
	return store.Session{
		ID: r.ID, Sub: r.Sub, Email: r.Email, Name: r.Name, Claims: []byte(r.Claims), InstanceAdmin: r.InstanceAdmin != 0,
		CreatedAt: created, LastSeenAt: seen, ExpiresAt: exp,
	}, nil
}

// CreateOIDCLogin implements store.SessionStore.
func (s *Store) CreateOIDCLogin(ctx context.Context, l store.OIDCLogin) error {
	if len(l.StateHash) != 64 || len(l.BindingHash) != 64 || l.Nonce == "" || l.Verifier == "" {
		return fmt.Errorf("a login needs hashed state and binding, a nonce and a verifier: %w", store.ErrInvalid)
	}
	err := s.q.InsertOIDCLogin(ctx, gen.InsertOIDCLoginParams{
		StateHash: l.StateHash, BindingHash: l.BindingHash, Nonce: l.Nonce, Verifier: l.Verifier, ReturnTo: l.ReturnTo, ExpiresAt: fmtTime(l.ExpiresAt),
	})
	if isUnique(err) {
		return fmt.Errorf("login state: %w", store.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("creating login: %w", err)
	}
	return nil
}

// ConsumeOIDCLogin implements store.SessionStore.
func (s *Store) ConsumeOIDCLogin(ctx context.Context, stateHash string, now time.Time) (store.OIDCLogin, error) {
	r, err := s.q.ConsumeOIDCLogin(ctx, gen.ConsumeOIDCLoginParams{StateHash: stateHash, Now: fmtTime(now)})
	if errors.Is(err, sql.ErrNoRows) {
		return store.OIDCLogin{}, fmt.Errorf("login: %w", store.ErrNotFound)
	}
	if err != nil {
		return store.OIDCLogin{}, fmt.Errorf("consuming login: %w", err)
	}
	exp, err := parseTime(r.ExpiresAt)
	if err != nil {
		return store.OIDCLogin{}, err
	}
	return store.OIDCLogin{
		StateHash: r.StateHash, BindingHash: r.BindingHash, Nonce: r.Nonce, Verifier: r.Verifier, ReturnTo: r.ReturnTo, ExpiresAt: exp,
	}, nil
}

// CreateSession implements store.SessionStore.
func (s *Store) CreateSession(ctx context.Context, n store.NewSession, now time.Time) error {
	if err := n.Validate(); err != nil {
		return err
	}
	err := s.q.InsertSession(ctx, gen.InsertSessionParams{
		ID: n.ID, TokenHash: n.TokenHash, Sub: n.Sub, Email: n.Email, Name: n.Name, Claims: string(n.Claims),
		InstanceAdmin: boolInt(n.InstanceAdmin), CreatedAt: fmtTime(now), ExpiresAt: fmtTime(n.ExpiresAt),
	})
	if isUnique(err) {
		return fmt.Errorf("session: %w", store.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

// FindSession implements store.SessionStore.
func (s *Store) FindSession(ctx context.Context, tokenHash string, now time.Time, idle time.Duration) (store.Session, error) {
	r, err := s.q.FindSession(ctx, gen.FindSessionParams{TokenHash: tokenHash, Now: fmtTime(now), IdleCutoff: fmtTime(now.Add(-idle))})
	if errors.Is(err, sql.ErrNoRows) {
		return store.Session{}, fmt.Errorf("session: %w", store.ErrNotFound)
	}
	if err != nil {
		return store.Session{}, fmt.Errorf("finding session: %w", err)
	}
	return toSession(r)
}

// TouchSession implements store.SessionStore.
func (s *Store) TouchSession(ctx context.Context, id string, now time.Time) error {
	if err := s.q.TouchSession(ctx, gen.TouchSessionParams{ID: id, Now: fmtTime(now)}); err != nil {
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
	ns, err := s.q.PurgeSessions(ctx, gen.PurgeSessionsParams{Now: fmtTime(now), IdleCutoff: fmtTime(now.Add(-idle))})
	if err != nil {
		return 0, 0, fmt.Errorf("purging sessions: %w", err)
	}
	nl, err := s.q.PurgeOIDCLogins(ctx, fmtTime(now))
	if err != nil {
		return ns, 0, fmt.Errorf("purging logins: %w", err)
	}
	return ns, nl, nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
