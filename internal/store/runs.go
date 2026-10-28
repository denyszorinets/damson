package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// RunStatus is the lifecycle state of a run.
type RunStatus string

// Run statuses.
const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
)

// Terminal reports whether no further transition is expected.
func (s RunStatus) Terminal() bool {
	return s == RunSucceeded || s == RunFailed || s == RunCanceled
}

// StepStatus is the lifecycle state of a step instance.
type StepStatus string

// Step instance statuses.
const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
	StepCanceled  StepStatus = "canceled"
)

// Terminal reports whether no further transition is expected.
func (s StepStatus) Terminal() bool {
	return s == StepSucceeded || s == StepFailed || s == StepSkipped || s == StepCanceled
}

// Step instance kinds.
const (
	KindStep  = "step"
	KindGroup = "group"
)

// Run is one invocation of a job version. Version is the optimistic
// concurrency counter every advance compares and bumps.
type Run struct {
	ID                string
	JobID             string
	JobVersionID      string
	Trigger           json.RawMessage
	Options           json.RawMessage // resolved options; secrets are refs only
	Status            RunStatus
	Version           int64
	CancelRequestedAt *time.Time
	ConcurrencyKey    string
	CreatedAt         time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
}

// StepInstance is one logical step of a run, addressed by its stable Path.
// Input is the resolved plugin input and must hold secret refs only, never
// secret values (ARCHITECTURE section 9); the store cannot verify that.
type StepInstance struct {
	ID         string
	RunID      string
	Path       string
	Kind       string
	Plugin     string
	Input      json.RawMessage
	Status     StepStatus
	Outputs    json.RawMessage
	Error      string
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// StepNode is a step instance with all its executions, oldest attempt first.
type StepNode struct {
	StepInstance
	Executions []Execution
}

// RunTree is a run with its step instances (creation order) and executions.
type RunTree struct {
	Run
	Steps []StepNode
}

// NewExecution describes the first (or a retry) execution of a step.
type NewExecution struct {
	Pool        string
	Priority    int
	ScheduledAt time.Time
}

// NewStepInstance describes a step instance to create. A nil Execution
// creates the step without scheduling it (groups, or steps scheduled later).
type NewStepInstance struct {
	Path      string
	Kind      string // defaults to KindStep
	Plugin    string
	Input     json.RawMessage
	Execution *NewExecution
}

// NewRun describes a run, its step instances and their first executions,
// created in one transaction.
type NewRun struct {
	// ID is optional; the store generates one when empty. A caller sets it
	// when the id must be known before the insert (it feeds expressions that
	// resolve step input, such as run.id).
	ID             string
	JobID          string
	JobVersionID   string
	Trigger        json.RawMessage
	Options        json.RawMessage
	ConcurrencyKey string
	Steps          []NewStepInstance
}

// StepUpdate is a CAS on a step instance's status.
type StepUpdate struct {
	ID             string
	ExpectedStatus StepStatus
	Status         StepStatus
	Outputs        json.RawMessage
	Error          string
}

// RunFilter narrows QueryRuns; a zero field matches everything.
type RunFilter struct {
	JobID  string
	Status RunStatus
}

// RunPage is one keyset page of runs, newest first.
type RunPage struct {
	Runs       []Run
	NextCursor string
}

// RunStore persists runs, step instances and their scheduling. The advance
// primitives (UpdateRunStatus, AddStepInstances, ScheduleExecution,
// RequestCancel) each compare and bump runs.version, so an advance built on a
// stale read loses with ErrStale and must reload (ARCHITECTURE section 8.3).
type RunStore interface {
	// CreateRun inserts the run (pending, version 1), its step instances and
	// the first executions in one transaction. It returns ErrNotFound if the
	// job or job version is absent, ErrConflict on a duplicate step path, and
	// ErrInvalid for malformed input.
	CreateRun(ctx context.Context, nr NewRun, now time.Time) (RunTree, error)
	// GetRun returns ErrNotFound if absent.
	GetRun(ctx context.Context, id string) (Run, error)
	// GetRunTree returns the run with its step instances and executions.
	GetRunTree(ctx context.Context, id string) (RunTree, error)
	// ListRuns lists a job's runs newest first; cursor is the previous
	// page's NextCursor ("" for the first page).
	ListRuns(ctx context.Context, jobID, cursor string, limit int) (RunPage, error)
	// ListActiveRunIDs returns up to limit ids of pending or running runs
	// with id > afterID, ascending (keyset; "" starts). It feeds the
	// run.advance safety-net sweep.
	ListActiveRunIDs(ctx context.Context, afterID string, limit int) ([]string, error)
	// QueryRuns is ListRuns across jobs: newest first, optionally filtered by
	// job and status. The cursor is the previous page's NextCursor.
	QueryRuns(ctx context.Context, f RunFilter, cursor string, limit int) (RunPage, error)
	// GetStepInstance returns ErrNotFound if absent.
	GetStepInstance(ctx context.Context, id string) (StepInstance, error)

	// Advance primitives reject a terminal run with ErrStale.
	//
	// UpdateRunStatus is a CAS on version. It sets started_at when the run
	// first moves to running and finished_at when it reaches a terminal
	// status, and returns the updated run (Version expectedVersion+1). It
	// returns ErrNotFound if absent and ErrStale on a version mismatch.
	UpdateRunStatus(ctx context.Context, runID string, expectedVersion int64, status RunStatus, now time.Time) (Run, error)
	// AddStepInstances creates step instances (and their first executions)
	// for an existing run and bumps the run version by one. ErrStale on a
	// version mismatch, ErrConflict on a duplicate path.
	AddStepInstances(ctx context.Context, runID string, expectedVersion int64, steps []NewStepInstance, now time.Time) ([]StepNode, error)
	// ScheduleExecution adds the next attempt of a step instance (retry) and
	// bumps the run version by one. ErrNotFound if the step is not in the
	// run; ErrConflict if its latest attempt is not yet terminal or lost.
	ScheduleExecution(ctx context.Context, runID string, expectedVersion int64, stepInstanceID string,
		ne NewExecution, now time.Time) (Execution, error)
	// UpdateStepInstance is a CAS on the step's status; it does not touch
	// the run version. ErrNotFound if absent, ErrStale if the status differs.
	UpdateStepInstance(ctx context.Context, u StepUpdate, now time.Time) (StepInstance, error)
	// RequestCancel sets cancel_requested_at and bumps the version. It
	// returns ErrNotFound if absent and ErrStale if cancellation was already
	// requested or the run is terminal.
	RequestCancel(ctx context.Context, runID string, now time.Time) (Run, error)
}

// ValidateNewRun rejects runs the store cannot persist, before any I/O.
func ValidateNewRun(nr NewRun) error {
	if nr.JobID == "" || nr.JobVersionID == "" {
		return fmt.Errorf("run needs a job and a job version: %w", ErrInvalid)
	}
	if nr.ID != "" {
		if _, err := uuid.Parse(nr.ID); err != nil {
			return fmt.Errorf("run id %q is not a uuid: %w", nr.ID, ErrInvalid)
		}
	}
	if err := checkJSON("trigger", nr.Trigger); err != nil {
		return err
	}
	if err := checkJSON("options", nr.Options); err != nil {
		return err
	}
	return ValidateSteps(nr.Steps)
}

// ValidateSteps rejects malformed step descriptions, including duplicate paths.
func ValidateSteps(steps []NewStepInstance) error {
	seen := make(map[string]bool, len(steps))
	for _, st := range steps {
		if st.Path == "" {
			return fmt.Errorf("step instance needs a path: %w", ErrInvalid)
		}
		if seen[st.Path] {
			return fmt.Errorf("duplicate step path %q: %w", st.Path, ErrConflict)
		}
		seen[st.Path] = true
		if err := checkJSON("step input", st.Input); err != nil {
			return err
		}
		if st.Execution != nil {
			if err := ValidateNewExecution(*st.Execution); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateNewExecution rejects an execution without a pool.
func ValidateNewExecution(ne NewExecution) error {
	if ne.Pool == "" {
		return fmt.Errorf("execution needs a pool: %w", ErrInvalid)
	}
	return nil
}

// KindOrDefault returns kind, or KindStep when empty.
func KindOrDefault(kind string) string {
	if kind == "" {
		return KindStep
	}
	return kind
}

// Stamps computes started_at and finished_at for a status change: started is
// set the first time running is true, finished the first time terminal is true.
func Stamps(started, finished *time.Time, running, terminal bool, now time.Time) (*time.Time, *time.Time) {
	n := NormalizeTime(now)
	if started == nil && running {
		started = &n
	}
	if finished == nil && terminal {
		finished = &n
	}
	return started, finished
}
