package engine

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time.Duration that marshals as a Go duration string.
type Duration time.Duration

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Action is something Decide wants applied to the database. The caller applies
// the actions of one Decide call in order, in a single transaction guarded by
// runs.version (§8.3).
type Action interface {
	// ActionType is the stable wire/golden name of the action.
	ActionType() string
}

// StartRun moves a queued run to running.
type StartRun struct {
	StartedAt time.Time `json:"started_at"`
}

// CreateStepInstance creates the StepInstance with its resolved input. The
// input contains no secret values: secret:// references stay literal (§9).
type CreateStepInstance struct {
	Path            string          `json:"path"`
	StepID          string          `json:"step_id"`
	Plugin          string          `json:"plugin"`
	Input           json.RawMessage `json:"input"`
	ContinueOnError bool            `json:"continue_on_error,omitempty"`
	RunsOn          *RunsOn         `json:"runs_on,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

// ScheduleExecution queues a new Execution (attempt) of an existing step
// instance. Timeout is data for the executor; 0 means none.
type ScheduleExecution struct {
	Path        string    `json:"path"`
	Attempt     int       `json:"attempt"`
	ScheduledAt time.Time `json:"scheduled_at"`
	Timeout     Duration  `json:"timeout,omitempty"`
}

// SkipStep records a step that will not run, creating its instance in the
// terminal status given (skipped, or cancelled when the run is cancelled).
type SkipStep struct {
	Path   string     `json:"path"`
	StepID string     `json:"step_id"`
	Status StepStatus `json:"status"`
	Reason string     `json:"reason"`
}

// FailStep records a step that failed before it could run, e.g. because its
// `when` or `with` expression failed to evaluate. It creates the instance in
// the failed status.
type FailStep struct {
	Path            string    `json:"path"`
	StepID          string    `json:"step_id"`
	ContinueOnError bool      `json:"continue_on_error,omitempty"`
	Error           StepError `json:"error"`
}

// CancelExecution asks the owning controller to cancel a running attempt.
type CancelExecution struct {
	Path        string `json:"path"`
	ExecutionID string `json:"execution_id"`
	AttemptID   string `json:"attempt_id"`
}

// CompleteRun finishes the run with its final status.
type CompleteRun struct {
	Status     RunStatus `json:"status"`
	FinishedAt time.Time `json:"finished_at"`
}

// ActionType implementations.
func (StartRun) ActionType() string           { return "start_run" }
func (CreateStepInstance) ActionType() string { return "create_step_instance" }
func (ScheduleExecution) ActionType() string  { return "schedule_execution" }
func (SkipStep) ActionType() string           { return "skip_step" }
func (FailStep) ActionType() string           { return "fail_step" }
func (CancelExecution) ActionType() string    { return "cancel_execution" }
func (CompleteRun) ActionType() string        { return "complete_run" }

// MarshalActions renders actions as a JSON array of objects, each with a
// leading "type" field. It is used for golden files and debug output.
func MarshalActions(actions []Action) ([]byte, error) {
	out := make([]map[string]any, 0, len(actions))
	for _, a := range actions {
		b, err := json.Marshal(a)
		if err != nil {
			return nil, fmt.Errorf("marshaling %s: %w", a.ActionType(), err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("remarshaling %s: %w", a.ActionType(), err)
		}
		m["type"] = a.ActionType()
		out = append(out, m)
	}
	return json.MarshalIndent(out, "", "  ")
}
