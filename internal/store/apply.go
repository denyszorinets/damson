package store

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Audit actions of jobs as code (ADR-0069).
const (
	ActionJobArchive    = "job.archive"
	ActionJobRestore    = "job.restore"
	ActionJobManage     = "job.manage"
	ActionProjectManage = "project.manage"
)

// Kinds of ApplyOp.
const (
	OpProjectCreate = "project.create"
	OpProjectUpdate = "project.update"
	OpJobCreate     = "job.create"
	OpJobUpdate     = "job.update"
	OpJobArchive    = "job.archive"
)

// SourceUI is the managed_by of objects nobody manages.
const SourceUI = "ui"

var sourceRE = regexp.MustCompile(`^(files|git):[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidSource reports whether s names a source: "files:<name>" or "git:<name>".
func ValidSource(s string) bool { return sourceRE.MatchString(s) }

// ApplyJob is a job as apply sees it: with its project's path, the checksum of
// its current version and the schedules and webhooks that still use it.
type ApplyJob struct {
	Job
	ProjectPath string
	Checksum    string
	// Bindings counts enabled schedules and unrevoked webhooks.
	Bindings int
}

// ApplyState is what a plan needs: every project, and the jobs of the named
// projects plus the jobs a source manages (archived ones included).
type ApplyState struct {
	Projects []Project
	Jobs     []ApplyJob
}

// ApplyOp is one write of an apply. Ops run in the order given, in one
// transaction, each as a compare-and-swap on the state the plan saw.
type ApplyOp struct {
	Kind string
	// Source is the managing source the op writes (create) or sets (update).
	Source string

	// Project ops. Create uses Path; update uses ID and ExpectVersion.
	Path          string
	ID            string
	ExpectVersion int
	Description   string
	AccessRules   []AccessRule
	NodeScope     string
	// ExpectManaged is the managed_by the object must still have.
	ExpectManaged string
	// ChangedDescription, ChangedRules and ChangedScope pick the audit events.
	ChangedDescription, ChangedRules, ChangedScope bool

	// Job ops. Create uses ProjectPath and Name; update and archive use ID and
	// ExpectCurrent. Spec is nil for an update that only changes management.
	ProjectPath   string
	Name          string
	Spec          json.RawMessage
	Checksum      string
	ExpectCurrent int
	Restore       bool
}

// ApplyStore reads the state of an apply and writes its plan.
type ApplyStore interface {
	// ApplyState returns all projects, the jobs of the projects at projectPaths
	// and the jobs whose managed_by is source.
	ApplyState(ctx context.Context, source string, projectPaths []string) (ApplyState, error)
	// ApplyPlan runs ops in one transaction and audits each for the actor in ctx
	// (ErrInvalid without one). A lost compare-and-swap rolls everything back
	// and returns ErrStale; a taken job name returns ErrConflict.
	ApplyPlan(ctx context.Context, ops []ApplyOp, now time.Time) error
}

// ValidateApplyOps rejects ops the store cannot run, before any I/O.
func ValidateApplyOps(ops []ApplyOp) error {
	for i, op := range ops {
		bad := func(format string, a ...any) error {
			return fmt.Errorf("apply op %d (%s): %s: %w", i, op.Kind, fmt.Sprintf(format, a...), ErrInvalid)
		}
		if !ValidSource(op.Source) && (op.Source != SourceUI || op.Kind == OpProjectCreate || op.Kind == OpJobCreate) {
			return bad("bad source %q", op.Source)
		}
		switch op.Kind {
		case OpProjectCreate:
			if err := ValidateProjectPath(op.Path); err != nil {
				return err
			}
		case OpProjectUpdate:
			if op.ID == "" || op.ExpectManaged == "" {
				return bad("needs an id and the expected management")
			}
		case OpJobCreate:
			if op.ProjectPath == "" || op.Name == "" {
				return bad("needs a project and a name")
			}
			if err := ValidateSpec(op.Spec, op.Checksum); err != nil {
				return err
			}
		case OpJobUpdate:
			if op.ID == "" || op.ExpectManaged == "" || op.ExpectCurrent < 1 {
				return bad("needs an id, the expected version and management")
			}
			if op.Spec != nil {
				if err := ValidateSpec(op.Spec, op.Checksum); err != nil {
					return err
				}
			}
		case OpJobArchive:
			if op.ID == "" || op.ExpectManaged == "" || op.ExpectCurrent < 1 {
				return bad("needs an id, the expected version and management")
			}
		default:
			return bad("unknown kind")
		}
		if strings.ContainsRune(op.Name, 0) {
			return bad("name holds a NUL byte")
		}
	}
	return nil
}

// ManageDetails is the details of job.manage and project.manage events.
type ManageDetails struct {
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
	From string `json:"from"`
	To   string `json:"to"`
}
