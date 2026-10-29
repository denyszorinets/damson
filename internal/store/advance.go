package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Tx is the transaction-scoped part of the store: the writes that must commit
// atomically with a River enqueue (ARCHITECTURE section 8.3, ADR-0010). Every
// call joins the one open transaction; nothing is visible to other readers
// until WithTx returns nil.
type Tx interface {
	// CreateRun and RequestCancel behave like their RunStore counterparts.
	CreateRun(ctx context.Context, nr NewRun, now time.Time) (RunTree, error)
	RequestCancel(ctx context.Context, runID string, now time.Time) (Run, error)
	// ApplyAdvance applies one run.advance result atomically: a single CAS on
	// runs.version (+1) guards every change in a. It returns ErrStale if the
	// version moved or the run is terminal, and ErrNotFound if the run is
	// absent. An empty a changes nothing and does not bump the version.
	ApplyAdvance(ctx context.Context, runID string, expectedVersion int64, a Advance, now time.Time) (Run, error)
	// GetRun reads the run inside the transaction.
	GetRun(ctx context.Context, runID string) (Run, error)
	// TouchRun bumps runs.version without any other change and returns the new
	// version. An event that must wake run.advance but changes no run column
	// (an execution completion) calls it, so the advance job keyed on the
	// version cannot be swallowed by one still running. ErrNotFound if the run
	// is absent, ErrStale if it is terminal.
	TouchRun(ctx context.Context, runID string) (int64, error)
	// CompleteExecution is the fenced completion of an attempt (see
	// ExecutionStore.CompleteExecution) that also moves its step instance to
	// the matching terminal status and bumps runs.version, all in this
	// transaction, so a completed attempt can never leave its step pending
	// (ARCHITECTURE 8.3 "Complete"). A finished run is left alone.
	CompleteExecution(ctx context.Context, c Completion, now time.Time) (Execution, error)
	// ExpireLeases is ExecutionStore.ExpireLeases with the same step and run
	// effects as CompleteExecution: each lost attempt fails its step.
	ExpireLeases(ctx context.Context, now time.Time, limit int) ([]Execution, error)
	// Native returns the driver transaction (pgx.Tx on Postgres, *sql.Tx on
	// SQLite) so River can InsertTx on it. Only the queue adapter uses it.
	Native() any
}

// TxRunner runs fn in one transaction: commit when fn returns nil, roll back
// on any error or panic. The Tx must not be used after fn returns.
type TxRunner interface {
	WithTx(ctx context.Context, fn func(tx Tx) error) error
}

// AdvanceStep is a step instance created by an advance. Status defaults to
// StepPending, in which case Execution schedules the first attempt. The other
// terminal statuses (skipped, failed, canceled) create an already finished
// instance with Reason stored as its error text and no execution.
type AdvanceStep struct {
	Path      string
	Plugin    string
	Input     json.RawMessage
	Status    StepStatus
	Reason    string
	Execution *NewExecution
}

// StatusOrDefault returns Status, or StepPending when empty.
func (s AdvanceStep) StatusOrDefault() StepStatus {
	if s.Status == "" {
		return StepPending
	}
	return s.Status
}

// AdvanceAttempt schedules a further attempt for an existing step instance.
type AdvanceAttempt struct {
	Path string
	NewExecution
}

// Advance is the dialect-neutral change set of one run.advance, applied by
// Tx.ApplyAdvance in this order: run start, new steps, new attempts, queued
// cancellations, run finish.
type Advance struct {
	// Start moves a pending run to running.
	Start bool
	// NewSteps are created in order.
	NewSteps []AdvanceStep
	// NewAttempts add an attempt to a step instance that has none yet.
	NewAttempts []AdvanceAttempt
	// CancelQueued lists execution ids to cancel while still queued. Their
	// step instances move to canceled in the same transaction. Executions
	// that were claimed in the meantime are left to their owning controller.
	CancelQueued []string
	// Finish, when set, moves the run to this terminal status.
	Finish RunStatus
}

// Empty reports whether applying a would change nothing.
func (a Advance) Empty() bool {
	return !a.Start && len(a.NewSteps) == 0 && len(a.NewAttempts) == 0 && len(a.CancelQueued) == 0 && a.Finish == ""
}

// ValidateAdvance rejects change sets the store cannot apply, before any I/O.
func ValidateAdvance(a Advance) error {
	if a.Finish != "" && !a.Finish.Terminal() {
		return fmt.Errorf("advance finish status %q is not terminal: %w", a.Finish, ErrInvalid)
	}
	seen := map[string]bool{}
	for _, st := range a.NewSteps {
		if st.Path == "" {
			return fmt.Errorf("advance step needs a path: %w", ErrInvalid)
		}
		if seen[st.Path] {
			return fmt.Errorf("duplicate advance step %q: %w", st.Path, ErrConflict)
		}
		seen[st.Path] = true
		if err := checkJSON("step input", st.Input); err != nil {
			return err
		}
		if err := validateAdvanceStep(st); err != nil {
			return err
		}
	}
	for _, at := range a.NewAttempts {
		if at.Path == "" {
			return fmt.Errorf("advance attempt needs a step path: %w", ErrInvalid)
		}
		if err := ValidateNewExecution(at.NewExecution); err != nil {
			return err
		}
	}
	return nil
}

func validateAdvanceStep(st AdvanceStep) error {
	switch st.StatusOrDefault() {
	case StepPending:
		if st.Execution == nil {
			return fmt.Errorf("pending advance step %q needs an execution: %w", st.Path, ErrInvalid)
		}
		return ValidateNewExecution(*st.Execution)
	case StepSkipped, StepFailed, StepCanceled:
		if st.Execution != nil {
			return fmt.Errorf("finished advance step %q cannot have an execution: %w", st.Path, ErrInvalid)
		}
		return nil
	}
	return fmt.Errorf("advance step %q has status %q: %w", st.Path, st.Status, ErrInvalid)
}

// AdvanceRunStatus computes the run status and timestamps an advance leaves
// behind. cur is the run as read inside the transaction. changed is false
// when neither Start nor Finish moves the status, so only the version bumps.
func AdvanceRunStatus(cur Run, a Advance, now time.Time) (status RunStatus, started, finished *time.Time, changed bool) {
	status = cur.Status
	if a.Start && status == RunPending {
		status = RunRunning
	}
	if a.Finish != "" {
		status = a.Finish
	}
	started, finished = Stamps(cur.StartedAt, cur.FinishedAt, status == RunRunning || status.Terminal(), status.Terminal(), now)
	return status, started, finished, status != cur.Status
}

// StepOutcome maps a finished attempt to the status and error text of its
// step instance. M3 retries will make some failures non-terminal for the
// step; until then a finished attempt finishes the step.
func StepOutcome(ex Execution) (StepStatus, string) {
	switch ex.State {
	case ExecSucceeded:
		return StepSucceeded, ""
	case ExecCanceled:
		return StepCanceled, ex.Error
	default: // failed, timed_out, lost
		reason := ex.Error
		if reason == "" {
			reason = string(ex.State)
		}
		return StepFailed, reason
	}
}
