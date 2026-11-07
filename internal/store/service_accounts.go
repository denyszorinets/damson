package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Roles of a service token, lowest first (ARCHITECTURE 12.4). A service token
// never holds the admin role: admin operations need the admin token until M8.
const (
	RoleViewer = "viewer"
	RoleRunner = "runner"
	RoleEditor = "editor"
)

// RoleRank orders roles; an unknown role ranks 0 and satisfies nothing.
func RoleRank(role string) int {
	switch role {
	case RoleViewer:
		return 1
	case RoleRunner:
		return 2
	case RoleEditor:
		return 3
	}
	return 0
}

// Audit actions of service accounts and their tokens.
const (
	ActionServiceAccountCreate = "service_account.create"
	ActionServiceAccountDelete = "service_account.delete"
	ActionServiceTokenCreate   = "service_token.create"
	ActionServiceTokenRevoke   = "service_token.revoke"
)

// Audit target types.
const (
	AuditTargetServiceAccount = "service_account"
	AuditTargetServiceToken   = "service_token"
)

// ServiceTokenTouchWindow is how often last_used_at is written at most.
const ServiceTokenTouchWindow = time.Minute

// ServiceAccount is a non-human identity that owns API tokens.
type ServiceAccount struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ServiceToken is a credential of a service account. The token and its hash are
// never part of this type. Project "" covers every project, otherwise the
// project path and its descendants.
type ServiceToken struct {
	ID               string
	ServiceAccountID string
	Name             string
	Project          string
	Role             string
	ExpiresAt        *time.Time
	RevokedAt        *time.Time
	LastUsedAt       *time.Time
	CreatedAt        time.Time
}

// NewServiceToken is a token to create with the SHA-256 hex of its secret.
type NewServiceToken struct {
	ServiceToken
	TokenHash string
}

// Validate checks the fields the store depends on.
func (n NewServiceToken) Validate() error {
	switch {
	case n.ID == "" || n.ServiceAccountID == "" || n.Name == "":
		return fmt.Errorf("a service token needs an id, an account and a name: %w", ErrInvalid)
	case len(n.TokenHash) != 64:
		return fmt.Errorf("a service token needs the SHA-256 hex of its secret: %w", ErrInvalid)
	case RoleRank(n.Role) == 0:
		return fmt.Errorf("service token role %q must be %s, %s or %s: %w", n.Role, RoleViewer, RoleRunner, RoleEditor, ErrInvalid)
	}
	return nil
}

// ServiceAuditDetails is the only content of service account and token events:
// names, scope and role. Never a token or a hash.
type ServiceAuditDetails struct {
	ServiceAccountID string `json:"service_account_id,omitempty"`
	Name             string `json:"name"`
	Project          string `json:"project,omitempty"`
	Role             string `json:"role,omitempty"`
	ExpiresAt        string `json:"expires_at,omitempty"`
}

// NewServiceEvent builds the audit event of a service account or token write.
func NewServiceEvent(ctx context.Context, action, targetType, target, project string, d ServiceAuditDetails, now time.Time) (AuditEvent, error) {
	meta, err := AuditFrom(ctx)
	if err != nil {
		return AuditEvent{}, err
	}
	id, err := NewID()
	if err != nil {
		return AuditEvent{}, err
	}
	details, err := json.Marshal(d)
	if err != nil {
		return AuditEvent{}, fmt.Errorf("encoding audit details: %w", err)
	}
	return AuditEvent{
		ID: id, At: NormalizeTime(now), Actor: meta.Actor, Action: action, TargetType: targetType,
		Target: target, ProjectPath: project, Details: details, RequestID: meta.RequestID,
	}, nil
}

// ServiceAccountStore persists service accounts and tokens. Writes are audited
// like ScheduleStore's.
type ServiceAccountStore interface {
	// CreateServiceAccount returns ErrConflict if the name is taken.
	CreateServiceAccount(ctx context.Context, a ServiceAccount, now time.Time) (ServiceAccount, error)
	// GetServiceAccount returns ErrNotFound if absent.
	GetServiceAccount(ctx context.Context, id string) (ServiceAccount, error)
	// ListServiceAccounts lists accounts in id order after afterID.
	ListServiceAccounts(ctx context.Context, afterID string, limit int) ([]ServiceAccount, error)
	// DeleteServiceAccount deletes the account and its tokens. ErrNotFound if absent.
	DeleteServiceAccount(ctx context.Context, id string, now time.Time) error
	// CreateServiceToken returns ErrNotFound if the account is absent.
	CreateServiceToken(ctx context.Context, n NewServiceToken, now time.Time) (ServiceToken, error)
	// GetServiceToken returns ErrNotFound if absent.
	GetServiceToken(ctx context.Context, id string) (ServiceToken, error)
	// ListServiceTokens lists the tokens of an account, revoked ones included.
	ListServiceTokens(ctx context.Context, accountID string) ([]ServiceToken, error)
	// RevokeServiceToken ends a token. ErrNotFound if absent; revoking a revoked
	// token is a no-op.
	RevokeServiceToken(ctx context.Context, id string, now time.Time) error
	// FindServiceToken returns the token whose secret hashes to tokenHash, or
	// ErrNotFound if it is unknown, revoked or expired at now.
	FindServiceToken(ctx context.Context, tokenHash string, now time.Time) (ServiceToken, error)
	// TouchServiceToken records a use (at most once per ServiceTokenTouchWindow).
	// It is best effort and never an error for a missing token.
	TouchServiceToken(ctx context.Context, id string, now time.Time) error
}
