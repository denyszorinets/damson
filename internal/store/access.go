package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Limits of a rule list.
const (
	MaxAccessRules     = 50
	MaxAccessRuleBytes = 2048
)

// Audit actions of access rules.
const (
	ActionProjectAccessRulesUpdate = "project.access_rules.update"
	ActionJobAccessRulesUpdate     = "job.access_rules.update"
	AuditTargetJob                 = "job"
)

// AccessRule grants Role to whoever the CEL expression Rule is true for
// (ARCHITECTURE 12.4). The store keeps the text; the API compiles it.
type AccessRule struct {
	Role string `json:"role"`
	Rule string `json:"rule"`
}

// EncodeAccessRules checks the shape of a rule list (count, size, roles up to
// maxRole) and returns its JSON. It does not compile the expressions.
func EncodeAccessRules(rules []AccessRule, maxRole string) (json.RawMessage, error) {
	if len(rules) > MaxAccessRules {
		return nil, fmt.Errorf("at most %d access rules: %w", MaxAccessRules, ErrInvalid)
	}
	for i, r := range rules {
		switch {
		case RoleRank(r.Role) == 0 || RoleRank(r.Role) > RoleRank(maxRole):
			return nil, fmt.Errorf("access rule %d: role %q is not allowed here: %w", i, r.Role, ErrInvalid)
		case r.Rule == "" || len(r.Rule) > MaxAccessRuleBytes:
			return nil, fmt.Errorf("access rule %d: the rule must be 1 to %d bytes: %w", i, MaxAccessRuleBytes, ErrInvalid)
		}
	}
	if rules == nil {
		rules = []AccessRule{}
	}
	return json.Marshal(rules)
}

// DecodeAccessRules parses a stored rule list.
func DecodeAccessRules(raw []byte) ([]AccessRule, error) {
	var rules []AccessRule
	if len(raw) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, fmt.Errorf("decoding access rules: %w", err)
	}
	return rules, nil
}

// JobAccess is the rules of one job. Absent rules read as version 0.
type JobAccess struct {
	JobID   string
	Rules   []AccessRule
	Version int
}

// JobRules is the rules of one job with its name and project, for finding the
// jobs a user reaches only through job-level rules.
type JobRules struct {
	JobID     string
	Name      string
	ProjectID string
	Rules     []AccessRule
}

// AccessAuditDetails is the content of access rule events: how many rules and
// which roles, never the expressions (they name groups and users).
type AccessAuditDetails struct {
	Path  string   `json:"path,omitempty"`
	JobID string   `json:"job_id,omitempty"`
	Count int      `json:"count"`
	Roles []string `json:"roles,omitempty"`
}

// NewAccessEvent builds the audit event of an access rule write.
func NewAccessEvent(ctx context.Context, action, targetType, target, project string, rules []AccessRule, jobID string, now time.Time) (AuditEvent, error) {
	meta, err := AuditFrom(ctx)
	if err != nil {
		return AuditEvent{}, err
	}
	id, err := NewID()
	if err != nil {
		return AuditEvent{}, err
	}
	d := AccessAuditDetails{Path: project, JobID: jobID, Count: len(rules)}
	for _, r := range rules {
		d.Roles = append(d.Roles, r.Role)
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

// AccessStore persists access rules. Writes are audited like ProjectStore's.
type AccessStore interface {
	// ListProjectChain returns the projects on path that exist, root first: the
	// project itself and its ancestors. A path that does not exist still yields
	// its existing ancestors.
	ListProjectChain(ctx context.Context, path string) ([]Project, error)
	// SetProjectAccessRules replaces a project's rules (roles up to admin) as a
	// CAS on the project version: ErrNotFound if absent, ErrStale if the version
	// moved, ErrInvalid for a malformed list.
	SetProjectAccessRules(ctx context.Context, id string, rules []AccessRule, expectedVersion int, now time.Time) (Project, error)
	// GetJobAccess returns the rules of a job; none is version 0 and no error.
	GetJobAccess(ctx context.Context, jobID string) (JobAccess, error)
	// ListJobRules returns up to limit live jobs that have rules of their own,
	// ordered by job id. Jobs with an empty rule list are not returned.
	ListJobRules(ctx context.Context, limit int) ([]JobRules, error)
	// SetJobAccessRules replaces a job's rules (roles up to editor) as a CAS on
	// the rules version: expectedVersion 0 creates them. ErrNotFound if the job
	// is absent, ErrStale if the version moved.
	SetJobAccessRules(ctx context.Context, jobID string, rules []AccessRule, expectedVersion int, now time.Time) (JobAccess, error)
}
