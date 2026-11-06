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
	// AppendAuditEvent inserts an audit event in this transaction.
	AppendAuditEvent(ctx context.Context, ev AuditEvent) error
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
	// CancelUndelivered ends a flagged attempt that was never delivered: it is
	// still dispatched and has CancelRequestedAt, so it becomes canceled, its
	// step instance too, and the run is touched. ErrStale if the attempt is not
	// current, not dispatched (a running attempt only ends by the agent's
	// report or the reaper) or not flagged; ErrNotFound if absent.
	CancelUndelivered(ctx context.Context, executionID, attemptID string, now time.Time) (Execution, error)
	// CheckLeader fences a singleton duty: it returns ErrStale unless holder
	// still holds the lease name at term and it has not expired at now. On
	// SQLite the transaction is exclusive, so the check is strict; on Postgres
	// it is best effort, which suffices for idempotent duties.
	CheckLeader(ctx context.Context, name, holder string, term int64, now time.Time) error
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
	Path string
	// Kind is KindStep (also when empty) or KindGroup. A group has no plugin,
	// input or execution; created with no status it starts running, with
	// started_at, and is finished later by Advance.FinishGroups. A group may
	// also be created already skipped, failed or canceled.
	Kind      string
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

// AdvanceFinishGroup finishes a group instance (succeeded, failed or canceled)
// once its steps are done. Error is the failure text that decided it, kept for
// a failed group and for a succeeded one whose failure its catch handled.
type AdvanceFinishGroup struct {
	Path   string
	Status StepStatus
	Error  string
}

// AdvanceAttempt schedules attempt number Attempt for an existing step
// instance: the first (when it has none yet) or a retry. The store applies it
// only if the step is still pending or running and its latest attempt is
// number Attempt-1 and finished; otherwise ErrConflict, so a duplicated or
// stale decision cannot create an attempt twice.
type AdvanceAttempt struct {
	Path    string
	Attempt int
	NewExecution
}

// AdvanceSettle finishes a step instance from the outcome of its attempt
// number Attempt, once the engine decided not to retry it (StepOutcome gives
// the status and error). The attempt must be the latest and finished, and the
// step still pending or running; otherwise ErrConflict.
type AdvanceSettle struct {
	Path    string
	Attempt int
}

// Advance is the dialect-neutral change set of one run.advance, applied by
// Tx.ApplyAdvance in this order: the run row (start or finish, and the version
// check) first, then the run cancel, new steps, settled steps, finished groups,
// new attempts and execution cancellations.
type Advance struct {
	// Start moves a pending run to running.
	Start bool
	// CancelRun, when set, records cancel_requested_at on the run with this
	// reason (CancelReasonTimeout), inside the advance's own version CAS. It
	// is how the job-level timeout becomes a sticky cancel.
	CancelRun string
	// NewSteps are created in order.
	NewSteps []AdvanceStep
	// Settle finishes steps whose failed attempt is not retried.
	Settle []AdvanceSettle
	// FinishGroups finishes groups whose steps are done. A group created in
	// the same advance can be finished in it.
	FinishGroups []AdvanceFinishGroup
	// NewAttempts add an attempt (the first, or a retry) to a step instance.
	NewAttempts []AdvanceAttempt
	// Cancel lists execution ids to cancel. A queued execution and its step
	// instance move to canceled in the same transaction. One that was claimed
	// (dispatched or running) is flagged with cancel_requested_at instead, and
	// its owning controller sends step.cancel; a terminal one is left alone.
	// The flag is set whatever the attempt id, because a claim between the
	// advance's read and its apply rotates it.
	Cancel []string
	// Finish, when set, moves the run to this terminal status.
	Finish RunStatus
}

// Empty reports whether applying a would change nothing.
func (a Advance) Empty() bool {
	return !a.Start && a.CancelRun == "" && len(a.NewSteps) == 0 && len(a.Settle) == 0 && len(a.FinishGroups) == 0 && len(a.NewAttempts) == 0 && len(a.Cancel) == 0 && a.Finish == ""
}

// ValidateAdvance rejects change sets the store cannot apply, before any I/O.
func ValidateAdvance(a Advance) error {
	if a.CancelRun != "" && a.CancelRun != CancelReasonTimeout {
		return fmt.Errorf("advance cancel reason %q is unknown: %w", a.CancelRun, ErrInvalid)
	}
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
	settled := map[string]bool{}
	for _, se := range a.Settle {
		if se.Path == "" || se.Attempt < 1 {
			return fmt.Errorf("advance settle needs a step path and an attempt number from 1: %w", ErrInvalid)
		}
		if settled[se.Path] {
			return fmt.Errorf("duplicate advance settle of step %q: %w", se.Path, ErrInvalid)
		}
		settled[se.Path] = true
	}
	finished := map[string]bool{}
	for _, fg := range a.FinishGroups {
		if fg.Path == "" {
			return fmt.Errorf("advance group finish needs a path: %w", ErrInvalid)
		}
		if fg.Status != StepSucceeded && fg.Status != StepFailed && fg.Status != StepCanceled {
			return fmt.Errorf("group %q cannot finish as %q: %w", fg.Path, fg.Status, ErrInvalid)
		}
		if finished[fg.Path] || settled[fg.Path] {
			return fmt.Errorf("duplicate advance finish of %q: %w", fg.Path, ErrInvalid)
		}
		finished[fg.Path] = true
	}
	scheduled := map[string]bool{}
	for _, at := range a.NewAttempts {
		if at.Path == "" || at.Attempt < 1 {
			return fmt.Errorf("advance attempt needs a step path and an attempt number from 1: %w", ErrInvalid)
		}
		if settled[at.Path] || finished[at.Path] {
			return fmt.Errorf("step %q is both settled and given a new attempt: %w", at.Path, ErrInvalid)
		}
		if scheduled[at.Path] {
			return fmt.Errorf("duplicate advance attempt for step %q: %w", at.Path, ErrInvalid)
		}
		scheduled[at.Path] = true
		if err := ValidateNewExecution(at.NewExecution); err != nil {
			return err
		}
	}
	return nil
}

// SettlesOnCompletion reports whether the store finishes a step in the
// transaction that records the end of an attempt in this state. A succeeded or
// canceled attempt has nothing left to decide. A failed, timed out or lost one
// leaves the step pending: the engine decides whether to retry it or to
// settle it (Advance.Settle).
func SettlesOnCompletion(s ExecutionState) bool {
	return s == ExecSucceeded || s == ExecCanceled
}

func validateAdvanceStep(st AdvanceStep) error {
	if st.Kind == KindGroup {
		// A for_each group keeps its frozen lane list in the input; a group never
		// has a plugin or an execution.
		if st.Plugin != "" || st.Execution != nil {
			return fmt.Errorf("advance group %q cannot have a plugin or an execution: %w", st.Path, ErrInvalid)
		}
		switch st.StatusOrDefault() {
		case StepPending, StepSkipped, StepFailed, StepCanceled:
			return nil
		}
		return fmt.Errorf("advance group %q has status %q: %w", st.Path, st.Status, ErrInvalid)
	}
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
// step instance. It is used when the step is settled: at once for a succeeded
// or canceled attempt, and for a failed, timed out or lost one when the engine
// decides not to retry it (Advance.Settle).
func StepOutcome(ex Execution) (StepStatus, string) {
	switch ex.State {
	case ExecSucceeded:
		return StepSucceeded, ""
	case ExecCanceled:
		return StepCanceled, ex.Error
	default: // failed, timed_out, lost
		// The step error carries the kind (timeout, lost, a plugin's error
		// code), so the engine's error.kind and retry_on can match on it.
		kind, msg := SplitStepError(ex.Error)
		if kind != "" {
			return StepFailed, ex.Error
		}
		if msg == "" {
			msg = string(ex.State)
		}
		return StepFailed, StepErrorText(ErrorKindOf(ex), msg)
	}
}
