package store

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DefaultProjectID and DefaultProjectPath identify the root project seeded by
// migration 00003.
const (
	DefaultProjectID   = "00000000-0000-7000-8000-000000000001"
	DefaultProjectPath = "default"
)

// Audit actions and target type of projects.
const (
	ActionProjectCreate = "project.create"
	ActionProjectUpdate = "project.update"
	ActionProjectDelete = "project.delete"

	AuditTargetProject = "project"
)

// MaxProjectDepth is the most segments a project path may have (ADR-0042).
const MaxProjectDepth = 8

var projectSegmentRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidateProjectPath accepts 1 to MaxProjectDepth segments of lowercase
// letters, digits and '-'. It is the grammar of secret scopes (ADR-0042) minus
// the reserved _inventory scope, which cannot be a project.
func ValidateProjectPath(path string) error {
	segs := strings.Split(path, "/")
	if len(segs) > MaxProjectDepth {
		return fmt.Errorf("project path has more than %d segments: %w", MaxProjectDepth, ErrInvalid)
	}
	for _, s := range segs {
		if !projectSegmentRE.MatchString(s) {
			return fmt.Errorf("project path %q: each segment must match %s: %w", path, projectSegmentRE, ErrInvalid)
		}
	}
	return nil
}

// ParentPath returns the path of path's parent, "" for a root.
func ParentPath(path string) string {
	i := strings.LastIndexByte(path, '/')
	if i < 0 {
		return ""
	}
	return path[:i]
}

// ProjectInUseError says why a project cannot be deleted. It matches
// ErrConflict; Error carries only the path and the kind of dependent.
type ProjectInUseError struct {
	Path string
	What string // "child projects", "jobs", "secrets" or "active tokens"
}

func (e *ProjectInUseError) Error() string {
	return fmt.Sprintf("project %q still has %s", e.Path, e.What)
}

// Unwrap makes the error match ErrConflict.
func (e *ProjectInUseError) Unwrap() error { return ErrConflict }

// Project is a node of the project tree. Its path never changes.
type Project struct {
	ID          string
	Path        string
	ParentID    string // "" for a root
	Description string
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	AccessRules []AccessRule
	// NodeScope is a CEL expression over a node; empty allows every node.
	NodeScope string
}

// ProjectAuditDetails is the only content of project events.
type ProjectAuditDetails struct {
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

// NewProjectEvent builds the audit event of a project write.
func NewProjectEvent(ctx context.Context, action, id string, d ProjectAuditDetails, now time.Time) (AuditEvent, error) {
	meta, err := AuditFrom(ctx)
	if err != nil {
		return AuditEvent{}, err
	}
	evID, err := NewID()
	if err != nil {
		return AuditEvent{}, err
	}
	details, err := json.Marshal(d)
	if err != nil {
		return AuditEvent{}, fmt.Errorf("encoding audit details: %w", err)
	}
	return AuditEvent{
		ID: evID, At: NormalizeTime(now), Actor: meta.Actor, Action: action, TargetType: AuditTargetProject,
		Target: id, ProjectPath: d.Path, Details: details, RequestID: meta.RequestID,
	}, nil
}

// ProjectStore persists the project tree. Writes are audited like
// ScheduleStore's: they need an actor from WithAudit.
type ProjectStore interface {
	// GetProjectByPath and GetProject return ErrNotFound if absent.
	GetProjectByPath(ctx context.Context, path string) (Project, error)
	GetProject(ctx context.Context, id string) (Project, error)
	// ListProjects returns every project ordered by path.
	ListProjects(ctx context.Context) ([]Project, error)
	// ListProjectSubtree returns the project and its descendants ordered by
	// path; empty when the path does not exist.
	ListProjectSubtree(ctx context.Context, path string) ([]Project, error)
	// CreateProject adds a project below its parent, which must exist
	// (ErrNotFound otherwise). ErrInvalid for a bad path, ErrConflict if the
	// path is taken.
	CreateProject(ctx context.Context, id, path, description string, now time.Time) (Project, error)
	// UpdateProject sets the description as a CAS on expectedVersion: ErrNotFound
	// if absent, ErrStale if the version moved.
	UpdateProject(ctx context.Context, id, description string, expectedVersion int, now time.Time) (Project, error)
	// DeleteProject removes a project. ErrNotFound if absent; ErrConflict (the
	// message names the reason) while it has children, jobs, secrets in its
	// scope or active tokens scoped to exactly its path.
	DeleteProject(ctx context.Context, id string, now time.Time) error
	// SetProjectNodeScope sets the node scope (empty clears it) as a CAS on the
	// project version: ErrNotFound if absent, ErrStale if the version moved,
	// ErrInvalid if it is longer than MaxNodeScopeBytes.
	SetProjectNodeScope(ctx context.Context, id, scope string, expectedVersion int, now time.Time) (Project, error)
}

// MaxNodeScopeBytes bounds a node scope expression.
const MaxNodeScopeBytes = 2048

// ActionProjectNodeScopeUpdate is the audit action of SetProjectNodeScope.
const ActionProjectNodeScopeUpdate = "project.node_scope.update"
