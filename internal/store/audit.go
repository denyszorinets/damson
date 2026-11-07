package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Audit actions.
const (
	ActionSecretCreate     = "secret.create"
	ActionSecretUpdate     = "secret.update"
	ActionSecretDelete     = "secret.delete"
	ActionSecretKEKRotate  = "secret.kek_rotate"
	ActionLogPurge         = "log.purge"
	ActionAdminTokenCreate = "admin_token.create"
	ActionAdminTokenRevoke = "admin_token.revoke"
)

// Audit target types.
const (
	AuditTargetSecret    = "secret"
	AuditTargetInstance  = "instance"
	AuditTargetExecution = "execution"
)

// AuditEvent is one row of the insert-only audit trail (ARCHITECTURE 12.4).
// Details is a JSON object built from a typed allowlist; it never holds a
// secret value.
type AuditEvent struct {
	ID          string
	At          time.Time
	Actor       string
	Action      string
	TargetType  string
	Target      string
	ProjectPath string
	Details     json.RawMessage
	RequestID   string
}

// AuditFilter selects events, newest first. BeforeAt/BeforeID is the keyset
// cursor (the last event of the previous page); a zero BeforeAt starts at the top.
type AuditFilter struct {
	TargetType string
	Target     string
	BeforeAt   time.Time
	BeforeID   string
}

// AuditStore reads the audit trail and appends standalone events. Events about
// a change are written inside the transaction of that change.
type AuditStore interface {
	// AppendAuditEvent inserts one event. ID, At, Actor, Action and TargetType must be set.
	AppendAuditEvent(ctx context.Context, ev AuditEvent) error
	// ListAuditEvents returns up to limit events, newest first.
	ListAuditEvents(ctx context.Context, f AuditFilter, limit int) ([]AuditEvent, error)
}

// AuditMeta is who made a change and under which request. It travels in the
// context, and a store write that is audited fails without it.
type AuditMeta struct {
	Actor     string
	RequestID string
}

type auditKey struct{}

// WithAudit returns ctx carrying the actor of the writes made with it.
func WithAudit(ctx context.Context, m AuditMeta) context.Context {
	return context.WithValue(ctx, auditKey{}, m)
}

// AuditFrom returns the actor in ctx, or ErrInvalid if there is none.
func AuditFrom(ctx context.Context) (AuditMeta, error) {
	m, _ := ctx.Value(auditKey{}).(AuditMeta)
	if m.Actor == "" {
		return m, fmt.Errorf("an audited write needs an actor in its context: %w", ErrInvalid)
	}
	return m, nil
}

// SecretAuditDetails is the only content of a secret event's details: ids,
// type, version and field names. Never values, and never built from a map.
type SecretAuditDetails struct {
	SecretID string   `json:"secret_id"`
	Type     string   `json:"type"`
	Version  int64    `json:"version"`
	Fields   []string `json:"fields,omitempty"`
	KEKID    string   `json:"kek_id,omitempty"`
}

// NewSecretEvent builds the event of a secret write for the actor in ctx.
func NewSecretEvent(ctx context.Context, action, scope, name string, d SecretAuditDetails, now time.Time) (AuditEvent, error) {
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
		ID: id, At: NormalizeTime(now), Actor: meta.Actor, Action: action, TargetType: AuditTargetSecret,
		Target: scope + "/" + name, ProjectPath: scope, Details: details, RequestID: meta.RequestID,
	}, nil
}

// ValidateAuditEvent rejects an event the store cannot record.
func ValidateAuditEvent(ev AuditEvent) error {
	if ev.ID == "" || ev.Actor == "" || ev.Action == "" || ev.TargetType == "" || ev.At.IsZero() {
		return fmt.Errorf("audit event needs id, time, actor, action and target type: %w", ErrInvalid)
	}
	if len(ev.Details) > 0 && !json.Valid(ev.Details) {
		return fmt.Errorf("audit details are not JSON: %w", ErrInvalid)
	}
	return nil
}

// KEKRotateAuditDetails is the details of a secret.kek_rotate event: counts and
// kek_ids only (ADR-0042 error texts name nothing else).
type KEKRotateAuditDetails struct {
	ActiveKEKID string `json:"active_kek_id"`
	Rewrapped   int    `json:"rewrapped"`
	Skipped     int    `json:"skipped"`
	Failed      int    `json:"failed"`
	CanaryMoved bool   `json:"canary_moved"`
	Stragglers  int    `json:"stragglers"`
	Error       string `json:"error,omitempty"`
}

// NewKEKRotateEvent builds the summary event of one rotate-kek run.
func NewKEKRotateEvent(ctx context.Context, d KEKRotateAuditDetails, now time.Time) (AuditEvent, error) {
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
		ID: id, At: NormalizeTime(now), Actor: meta.Actor, Action: ActionSecretKEKRotate, TargetType: AuditTargetInstance,
		Target: "kek", Details: details, RequestID: meta.RequestID,
	}, nil
}

// NewLogPurgeEvent builds the event of a log purge for the actor in ctx.
func NewLogPurgeEvent(ctx context.Context, d LogPurgeAuditDetails, now time.Time) (AuditEvent, error) {
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
		ID: id, At: NormalizeTime(now), Actor: meta.Actor, Action: ActionLogPurge, TargetType: AuditTargetExecution,
		Target: d.ExecutionID, Details: details, RequestID: meta.RequestID,
	}, nil
}

// NewAdminTokenEvent builds the event of an admin token create or revoke
// (action is ActionAdminTokenCreate or ActionAdminTokenRevoke). It carries no
// details: neither the token nor its hash is recorded.
func NewAdminTokenEvent(ctx context.Context, action string, now time.Time) (AuditEvent, error) {
	meta, err := AuditFrom(ctx)
	if err != nil {
		return AuditEvent{}, err
	}
	id, err := NewID()
	if err != nil {
		return AuditEvent{}, err
	}
	return AuditEvent{
		ID: id, At: NormalizeTime(now), Actor: meta.Actor, Action: action, TargetType: AuditTargetInstance,
		Target: "admin_token", Details: json.RawMessage(`{}`), RequestID: meta.RequestID,
	}, nil
}
