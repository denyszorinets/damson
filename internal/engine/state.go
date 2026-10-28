package engine

import (
	"time"
)

// RunStatus is the lifecycle state of a run (§8.3).
type RunStatus string

// Run statuses.
const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

// Terminal reports whether the run can no longer change.
func (s RunStatus) Terminal() bool {
	return s == RunSucceeded || s == RunFailed || s == RunCancelled
}

// StepStatus is the state of a StepInstance.
type StepStatus string

// Step statuses. Pending covers "created, execution not finished": queued,
// dispatched and running are Execution states, not step states.
const (
	StepPending   StepStatus = "pending"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
	StepCancelled StepStatus = "cancelled"
)

// Terminal reports whether the step instance can no longer change.
func (s StepStatus) Terminal() bool { return s != StepPending && s != "" }

// ExecState is the state of one Execution (an attempt of a step instance).
type ExecState string

// Execution states.
const (
	ExecQueued     ExecState = "queued"
	ExecDispatched ExecState = "dispatched"
	ExecRunning    ExecState = "running"
	ExecSucceeded  ExecState = "succeeded"
	ExecFailed     ExecState = "failed"
	ExecCancelled  ExecState = "cancelled"
	ExecLost       ExecState = "lost"
)

// Terminal reports whether the execution is finished.
func (s ExecState) Terminal() bool {
	switch s {
	case ExecSucceeded, ExecFailed, ExecCancelled, ExecLost:
		return true
	}
	return false
}

// RunState is everything Decide knows about a run. The store builds it from
// the database; Decide never reads anything else. Steps lists the step
// instances that exist so far, in creation order.
type RunState struct {
	ID          string         `json:"id"`
	Status      RunStatus      `json:"status"`
	Trigger     string         `json:"trigger,omitempty"`
	TriggeredBy string         `json:"triggered_by,omitempty"`
	Job         JobInfo        `json:"job"`
	Inputs      map[string]any `json:"inputs,omitempty"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	// CancelRequestedAt is runs.cancel_requested_at; non-nil means cancel.
	CancelRequestedAt *time.Time          `json:"cancel_requested_at,omitempty"`
	Steps             []StepInstanceState `json:"steps,omitempty"`
}

// JobInfo is the job identity exposed to expressions as `job`.
type JobInfo struct {
	Name    string `json:"name"`
	Project string `json:"project"`
	Version string `json:"version"`
}

// StepInstanceState is the view of one StepInstance.
type StepInstanceState struct {
	// Path is the stable instance path. For top-level steps it equals the
	// step id; M3 extends it for groups and lanes ("main/for_each[web-01]/x").
	Path       string          `json:"path"`
	StepID     string          `json:"step_id"`
	Status     StepStatus      `json:"status"`
	Outputs    map[string]any  `json:"outputs,omitempty"`
	Error      *StepError      `json:"error,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Executions []ExecutionView `json:"executions,omitempty"`
}

// ExecutionView is the view of one Execution (attempt).
type ExecutionView struct {
	ID              string    `json:"id"`
	AttemptID       string    `json:"attempt_id"`
	State           ExecState `json:"state"`
	CancelRequested bool      `json:"cancel_requested,omitempty"`
}

// StepError describes why a step failed.
type StepError struct {
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

// Error kinds produced by the engine itself.
const (
	ErrKindExpression = "expression"
)

func (s StepInstanceState) hasActiveExecution() bool {
	for _, e := range s.Executions {
		if !e.State.Terminal() {
			return true
		}
	}
	return false
}
