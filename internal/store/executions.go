package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ExecutionState is the state of one execution attempt.
type ExecutionState string

// Execution states.
const (
	ExecQueued     ExecutionState = "queued"
	ExecDispatched ExecutionState = "dispatched"
	ExecRunning    ExecutionState = "running"
	ExecSucceeded  ExecutionState = "succeeded"
	ExecFailed     ExecutionState = "failed"
	ExecCanceled   ExecutionState = "canceled"
	ExecTimedOut   ExecutionState = "timed_out"
	ExecLost       ExecutionState = "lost"
)

// Terminal reports whether the attempt is finished.
func (s ExecutionState) Terminal() bool {
	switch s {
	case ExecSucceeded, ExecFailed, ExecCanceled, ExecTimedOut, ExecLost:
		return true
	}
	return false
}

// Reportable reports whether an agent may complete an attempt into this state
// (lost is set only by the reaper).
func (s ExecutionState) Reportable() bool { return s.Terminal() && s != ExecLost }

// Execution is one attempt of a step instance. AttemptID is the fencing
// token: it is rotated at every dispatch claim and is the only value under
// which an agent's reports are accepted (ARCHITECTURE section 7.3).
type Execution struct {
	ID             string
	RunID          string
	StepInstanceID string
	AttemptNo      int
	AttemptID      string
	State          ExecutionState
	ScheduledAt    time.Time
	Priority       int
	Pool           string
	AgentID        string
	ControllerID   string
	LeaseExpiresAt *time.Time
	Outputs        json.RawMessage
	Error          string
	CreatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

// ClaimParams selects queued executions for one connected agent.
type ClaimParams struct {
	AgentID      string
	ControllerID string
	// Pools are the pools the agent serves; executions of all of them compete
	// in one global order (priority, scheduled_at). Label and capability
	// matching is the dispatcher's job (M3), not the claim's.
	Pools    []string
	Limit    int // free slots on the agent, clamped to MaxPageLimit
	LeaseTTL time.Duration
}

// Completion is an agent's terminal report for an attempt.
type Completion struct {
	ExecutionID string
	AttemptID   string
	State       ExecutionState // must be Reportable
	Outputs     json.RawMessage
	Error       string
}

// ExecutionStore owns the dispatch, lease and completion transitions. Every
// transition after the claim is fenced by attempt_id: a stale attempt returns
// ErrStale and never mutates anything.
type ExecutionStore interface {
	// GetExecution and GetExecutionByAttempt return ErrNotFound if absent.
	GetExecution(ctx context.Context, id string) (Execution, error)
	GetExecutionByAttempt(ctx context.Context, attemptID string) (Execution, error)

	// ClaimExecutions atomically moves up to Limit queued, due executions of
	// the pools to dispatched, assigning the agent and controller, a fresh
	// attempt_id and a lease of now+LeaseTTL. Concurrent claimers never get the
	// same execution (FOR UPDATE SKIP LOCKED on Postgres, serialized
	// transactions on SQLite). It returns the claimed executions, possibly none.
	ClaimExecutions(ctx context.Context, p ClaimParams, now time.Time) ([]Execution, error)
	// MarkExecutionRunning moves dispatched to running. ErrNotFound if the
	// execution is absent; ErrStale if the attempt_id or state does not match.
	MarkExecutionRunning(ctx context.Context, executionID, attemptID string, now time.Time) error
	// RenewLeases is one batched update: it extends (never shortens) the lease
	// of every listed attempt that is still dispatched or running and owned
	// by controllerID, and returns the ids of those attempts. The rest are
	// stale, with no error.
	RenewLeases(ctx context.Context, controllerID string, attemptIDs []string, leaseExpiresAt time.Time) ([]string, error)
	// ExpireLeases marks up to limit dispatched or running executions whose
	// lease ended before now as lost, and returns them (the reaper).
	ExpireLeases(ctx context.Context, now time.Time, limit int) ([]Execution, error)
	// CompleteExecution sets the terminal state, outputs and error. It
	// returns ErrNotFound if absent and ErrStale if the attempt_id is not
	// current or the execution is no longer dispatched or running; nothing
	// is changed in that case. ErrInvalid for a non-reportable state.
	CompleteExecution(ctx context.Context, c Completion, now time.Time) (Execution, error)
	// RequeueExecution moves dispatched back to queued (agent rejected or
	// the send failed), clearing ownership. Fenced like CompleteExecution.
	RequeueExecution(ctx context.Context, executionID, attemptID string, scheduledAt time.Time) error
}

// ValidateCompletion rejects completions the store cannot apply.
func ValidateCompletion(c Completion) error {
	if c.ExecutionID == "" || c.AttemptID == "" {
		return fmt.Errorf("completion needs an execution id and attempt id: %w", ErrInvalid)
	}
	if !c.State.Reportable() {
		return fmt.Errorf("completion state %q is not a reportable terminal state: %w", c.State, ErrInvalid)
	}
	return checkJSON("outputs", c.Outputs)
}

// ValidateClaim rejects claims the store cannot apply.
func ValidateClaim(p ClaimParams) error {
	if p.AgentID == "" || p.ControllerID == "" || len(p.Pools) == 0 || p.Limit <= 0 || p.LeaseTTL <= 0 {
		return fmt.Errorf("claim needs agent, controller, pools, positive limit and lease ttl: %w", ErrInvalid)
	}
	for _, pool := range p.Pools {
		if pool == "" {
			return fmt.Errorf("claim has an empty pool name: %w", ErrInvalid)
		}
	}
	return nil
}
