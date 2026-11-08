package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// MaxSessionClaimsBytes bounds the claims snapshot of a session.
const MaxSessionClaimsBytes = 64 << 10

// OIDCLogin is the state of one login in progress (ADR-0063). It is keyed by
// the SHA-256 hex of the state parameter and consumed by the callback.
type OIDCLogin struct {
	StateHash   string
	BindingHash string // SHA-256 hex of the pre-login cookie value
	Nonce       string
	Verifier    string // PKCE code verifier
	ReturnTo    string
	ExpiresAt   time.Time
}

// Session is a logged-in browser session. Claims are the id token's claims,
// frozen at login.
type Session struct {
	ID            string
	Sub           string
	Email         string
	Name          string
	Claims        json.RawMessage
	InstanceAdmin bool
	CreatedAt     time.Time
	LastSeenAt    time.Time
	ExpiresAt     time.Time
}

// NewSession is a session to create with the SHA-256 hex of its cookie value.
type NewSession struct {
	Session
	TokenHash string
}

// Validate checks the fields the store depends on.
func (n NewSession) Validate() error {
	switch {
	case n.ID == "" || n.Sub == "":
		return fmt.Errorf("a session needs an id and a subject: %w", ErrInvalid)
	case len(n.TokenHash) != 64:
		return fmt.Errorf("a session needs the SHA-256 hex of its token: %w", ErrInvalid)
	case !n.ExpiresAt.After(n.CreatedAt):
		return fmt.Errorf("a session must expire after it is created: %w", ErrInvalid)
	case len(n.Claims) > MaxSessionClaimsBytes:
		return fmt.Errorf("session claims exceed %d bytes: %w", MaxSessionClaimsBytes, ErrInvalid)
	}
	return checkJSON("session claims", n.Claims)
}

// SessionStore persists login state and sessions. Neither is audited here: the
// login event is written by the API (ADR-0053).
type SessionStore interface {
	// CreateOIDCLogin stores a login in progress. ErrConflict if the state is taken.
	CreateOIDCLogin(ctx context.Context, l OIDCLogin) error
	// ConsumeOIDCLogin deletes and returns the login of the state hash, so it
	// works once. ErrNotFound if it is unknown, used or expired at now.
	ConsumeOIDCLogin(ctx context.Context, stateHash string, now time.Time) (OIDCLogin, error)
	// CreateSession inserts a session.
	CreateSession(ctx context.Context, n NewSession, now time.Time) error
	// FindSession returns the session of the token hash if it has not expired
	// and was seen within idle of now. ErrNotFound otherwise.
	FindSession(ctx context.Context, tokenHash string, now time.Time, idle time.Duration) (Session, error)
	// TouchSession moves last_seen_at forward to now.
	TouchSession(ctx context.Context, id string, now time.Time) error
	// DeleteSession ends the session of the token hash; absent is not an error.
	DeleteSession(ctx context.Context, tokenHash string) error
	// PurgeExpiredAuth deletes sessions past their expiry or idle limit and
	// expired logins. The rows were already unusable, so no fencing is needed.
	PurgeExpiredAuth(ctx context.Context, now time.Time, idle time.Duration) (sessions, logins int64, err error)
}
