package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Audit actions of webhooks.
const (
	ActionWebhookCreate = "webhook.create"
	ActionWebhookRevoke = "webhook.revoke"
)

// AuditTargetWebhook is the audit target type of webhook events.
const AuditTargetWebhook = "webhook"

// WebhookTouchWindow is how often last_used_at is written at most.
const WebhookTouchWindow = time.Minute

// Webhook starts runs of a job when its token is posted to (ARCHITECTURE 12.3).
// The token itself is never stored, only its hash, which this type leaves out.
type Webhook struct {
	ID             string
	JobID          string
	Name           string
	Options        json.RawMessage // static options, a JSON object
	OptionsMapping string          // CEL over the request, or empty
	RevokedAt      *time.Time
	LastUsedAt     *time.Time
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewWebhook is a webhook to create with the SHA-256 hex of its token.
type NewWebhook struct {
	Webhook
	TokenHash string
}

// Validate checks the fields the store depends on.
func (n NewWebhook) Validate() error {
	switch {
	case n.ID == "" || n.JobID == "" || n.Name == "":
		return fmt.Errorf("a webhook needs an id, a job and a name: %w", ErrInvalid)
	case len(n.TokenHash) != 64:
		return fmt.Errorf("a webhook needs the SHA-256 hex of its token: %w", ErrInvalid)
	case len(n.Options) > 0 && !json.Valid(n.Options):
		return fmt.Errorf("webhook options are not JSON: %w", ErrInvalid)
	}
	return nil
}

// WebhookAuditDetails is the only content of a webhook event's details. The
// token and its hash are never in it, and neither are the options.
type WebhookAuditDetails struct {
	JobID      string `json:"job_id"`
	Name       string `json:"name"`
	HasMapping bool   `json:"has_mapping"`
}

// NewWebhookEvent builds the event of a webhook write for the actor in ctx.
func NewWebhookEvent(ctx context.Context, action string, w Webhook, now time.Time) (AuditEvent, error) {
	meta, err := AuditFrom(ctx)
	if err != nil {
		return AuditEvent{}, err
	}
	id, err := NewID()
	if err != nil {
		return AuditEvent{}, err
	}
	details, err := json.Marshal(WebhookAuditDetails{JobID: w.JobID, Name: w.Name, HasMapping: w.OptionsMapping != ""})
	if err != nil {
		return AuditEvent{}, fmt.Errorf("encoding audit details: %w", err)
	}
	return AuditEvent{
		ID: id, At: NormalizeTime(now), Actor: meta.Actor, Action: action, TargetType: AuditTargetWebhook,
		Target: w.ID, Details: details, RequestID: meta.RequestID,
	}, nil
}

// WebhookStore persists webhooks. Writes are audited like ScheduleStore's.
type WebhookStore interface {
	// CreateWebhook returns ErrNotFound if the job is absent and ErrConflict if
	// the job has an active webhook of that name (or the token hash exists).
	CreateWebhook(ctx context.Context, n NewWebhook, now time.Time) (Webhook, error)
	// GetWebhook returns ErrNotFound if absent.
	GetWebhook(ctx context.Context, id string) (Webhook, error)
	// ListWebhooks lists the webhooks of a job (all when jobID is empty), revoked
	// ones included, in id order after afterID.
	ListWebhooks(ctx context.Context, jobID, afterID string, limit int) ([]Webhook, error)
	// FindWebhook returns the active webhook whose token hashes to tokenHash, or
	// ErrNotFound for an unknown or revoked token.
	FindWebhook(ctx context.Context, tokenHash string) (Webhook, error)
	// RevokeWebhook stops the token from working. ErrNotFound if absent;
	// revoking a revoked webhook is a no-op.
	RevokeWebhook(ctx context.Context, id string, now time.Time) error
}
