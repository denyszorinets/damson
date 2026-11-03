package engine

import (
	"fmt"
	"slices"
	"time"
)

// defaultRetryKinds are the error kinds retried when a retry block has no
// retry_on (ARCHITECTURE 5.3 rule 7).
var defaultRetryKinds = []string{"connection", ErrKindTimeout, "lost"}

// attend handles a step instance that is not finished yet. Its latest attempt
// (the one with the highest number) decides what is next:
//
//   - no attempt (cannot happen when actions are applied atomically): schedule
//     the first one;
//   - the attempt is queued or running: wait;
//   - the attempt failed, timed out or was lost: the store left the step
//     pending (a completion settles the step itself only for succeeded and
//     canceled attempts), and the engine either schedules the next attempt or
//     emits SettleStep so the store finishes the step from that attempt.
//
// Both ways end this Decide: the advance runs again and reads the result.
func (d *decider) attend(sc *scope, step Step, inst StepInstanceState) error {
	latest := latestExecution(inst)
	if latest == nil {
		timeout, err := parseTimeout(step)
		if err != nil {
			return err
		}
		d.emit(ScheduleExecution{Path: inst.Path, Attempt: 1, ScheduledAt: d.now, Timeout: timeout})
		return nil
	}
	if !latest.State.Terminal() || (latest.State != ExecFailed && latest.State != ExecLost) {
		// In flight, or finished in a way the store settles itself.
		return nil
	}
	at, retry, err := d.retryDecision(sc, step, inst, latest)
	if err != nil {
		return err
	}
	if !retry {
		d.emit(SettleStep{Path: inst.Path, Attempt: latest.Attempt})
		return nil
	}
	timeout, err := parseTimeout(step)
	if err != nil {
		return err
	}
	d.emit(ScheduleExecution{Path: inst.Path, Attempt: latest.Attempt + 1, ScheduledAt: at, Timeout: timeout})
	return nil
}

// latestExecution is the attempt with the highest number, or nil.
func latestExecution(inst StepInstanceState) *ExecutionView {
	var latest *ExecutionView
	for i := range inst.Executions {
		if latest == nil || inst.Executions[i].Attempt > latest.Attempt {
			latest = &inst.Executions[i]
		}
	}
	return latest
}

// retryDecision decides whether the failed or lost attempt `latest` is retried
// and, if so, when the next attempt becomes due. The order of the rules is
// ADR-0036's. Everything it reads is in the state, so the same state always
// gives the same answer and `at`.
func (d *decider) retryDecision(sc *scope, step Step, inst StepInstanceState, latest *ExecutionView) (at time.Time, retry bool, err error) {
	switch {
	case d.canceling: // a user cancel or the job timeout: no further attempts, not even for cleanup steps
		return at, false, nil
	case step.Retry == nil: // without a retry block there is one attempt
		return at, false, nil
	case latest.Attempt >= step.Retry.MaxAttempts:
		return at, false, nil
	case latest.State == ExecLost && !d.lostMayRetry(step):
		return at, false, nil
	}
	ok, err := d.retryOn(sc, step, inst, latest)
	if err != nil || !ok {
		return at, false, err
	}
	base := d.now
	if latest.FinishedAt != nil {
		base = *latest.FinishedAt
	}
	next := latest.Attempt + 1
	delay := backoffDelay(backoffOf(step.Retry), latest.Attempt, jitterUnit(d.st.ID, inst.Path, next))
	at = base.Add(delay)
	if deadline, has := d.jobDeadline(); has && !at.Before(deadline) {
		// The retry would start after the job timeout: fail with the real
		// error now instead of a later "job timed out" cancel.
		return at, false, nil
	}
	return at, true, nil
}

// lostMayRetry applies on_lost: set, it decides; unset, the plugin's
// idempotent flag does.
func (d *decider) lostMayRetry(step Step) bool {
	if step.Retry.OnLost != nil {
		return *step.Retry.OnLost
	}
	return d.st.IdempotentPlugins[step.Plugin]
}

// retryOn evaluates retry_on over the attempt's error (the default list when
// absent). An expression that fails to evaluate means no retry, so the step
// settles with the attempt's own error and catch and continue_on_error still
// see the real kind.
func (d *decider) retryOn(sc *scope, step Step, inst StepInstanceState, latest *ExecutionView) (bool, error) {
	kind, message := attemptError(latest)
	if step.Retry.RetryOn == "" {
		return slices.Contains(defaultRetryKinds, kind), nil
	}
	errv := map[string]any{"step": inst.StepID, "kind": kind, "message": message}
	if latest.Error != nil && latest.Error.ExitCode != nil {
		errv["exit_code"] = float64(*latest.Error.ExitCode)
	}
	vars := d.vars(sc, false)
	vars.Error = errv
	ok, err := d.e.env.EvalBool(step.Retry.RetryOn, vars)
	if err != nil {
		return false, nil //nolint:nilerr // documented: an evaluation error means no retry
	}
	return ok, nil
}

// attemptError is the kind and message of a failed attempt.
func attemptError(ex *ExecutionView) (kind, message string) {
	if ex.Error != nil {
		return ex.Error.Kind, ex.Error.Message
	}
	if ex.State == ExecLost {
		return "lost", "lease expired"
	}
	return "error", fmt.Sprintf("attempt %d failed", ex.Attempt)
}

// jobDeadline is runs.started_at + spec.timeout, when the job has a timeout.
func (d *decider) jobDeadline() (time.Time, bool) {
	if d.job.Spec.Timeout == "" {
		return time.Time{}, false
	}
	timeout, err := time.ParseDuration(d.job.Spec.Timeout)
	if err != nil || timeout <= 0 {
		return time.Time{}, false
	}
	return d.startedAt.Add(timeout), true
}
