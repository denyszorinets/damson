package engine

import (
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/expr"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// defaultWhen is the condition of a step without `when` (§5.2).
const defaultWhen = "success()"

// Engine evaluates the control-flow rules of §5.3. It holds only the
// compiled-expression environment, so it is safe for concurrent use and
// Decide stays a pure function of its arguments.
type Engine struct {
	env       *expr.Env
	jobSchema *jsonschema.Schema
}

// New builds an Engine whose expressions run under the given limits.
func New(limits expr.Limits) (*Engine, error) {
	env, err := expr.NewEnv(limits)
	if err != nil {
		return nil, err
	}
	schema, err := compileJobSchema()
	if err != nil {
		return nil, err
	}
	return &Engine{env: env, jobSchema: schema}, nil
}

// Decide computes the next actions for a run. job must have passed Parse or
// Validate. Decide never mutates its arguments, performs no I/O and reads no
// clock: the same (job, state, now) always yields the same actions.
//
// Sequential semantics (§5.3 rules 1-3, 8):
//   - steps run in order; a step is created only once all earlier steps are
//     terminal, then its `when` (default success()) decides run or skip;
//   - after an unhandled failure the rest of the list is skipped, except
//     steps whose `when` calls failure(), always() or cancelled();
//   - a failed step with continue_on_error does not fail the run;
//   - on cancel, running executions of earlier steps are cancelled and the
//     remaining steps are cancelled unless their `when` allows it.
//
// An empty result means "wait": an execution is in flight.
func (e *Engine) Decide(job *Job, st RunState, now time.Time) ([]Action, error) {
	if st.Status.Terminal() {
		return nil, nil
	}
	d := &decider{e: e, job: job, st: st, now: now, canceling: st.CancelRequestedAt != nil, cancelAt: st.CancelRequestedAt,
		timedOut: st.CancelRequestedAt != nil && st.CancelReason == CancelReasonTimeout}
	if err := d.index(); err != nil {
		return nil, err
	}
	return d.decide()
}

type decider struct {
	e         *Engine
	job       *Job
	st        RunState
	now       time.Time
	canceling bool
	cancelAt  *time.Time // when the cancel was requested (this Decide may set it: job timeout)
	timedOut  bool       // the cancel is the job-level timeout

	byID      map[string]StepInstanceState
	runStatus RunStatus
	startedAt time.Time
	steps     map[string]any // the `steps` expression variable, built as we walk
	acts      []Action
}

func (d *decider) index() error {
	known := map[string]bool{}
	for _, s := range d.job.Spec.Steps {
		known[s.ID] = true
	}
	d.byID = make(map[string]StepInstanceState, len(d.st.Steps))
	for _, inst := range d.st.Steps {
		if !known[inst.StepID] {
			return fmt.Errorf("run %s: step instance %q has no step %q in the job spec", d.st.ID, inst.Path, inst.StepID)
		}
		if _, dup := d.byID[inst.StepID]; dup {
			return fmt.Errorf("run %s: duplicate step instance for step %q", d.st.ID, inst.StepID)
		}
		d.byID[inst.StepID] = inst
	}
	d.steps = map[string]any{}
	return nil
}

func (d *decider) emit(a ...Action) { d.acts = append(d.acts, a...) }

// decide is the sequential-scope kernel: one flat loop over one step list.
// In M3 it is re-keyed by Path and recursed per scope (groups, catch/finally,
// for_each lanes) rather than rewritten; the helpers below already take the
// step and its instance, not a list index.
func (d *decider) decide() ([]Action, error) {
	d.runStatus = d.st.Status
	d.startedAt = d.now
	if d.st.StartedAt != nil {
		d.startedAt = *d.st.StartedAt
	}
	starting := false
	if d.runStatus == RunQueued {
		d.emit(StartRun{StartedAt: d.now})
		d.runStatus = RunRunning
		starting = true
	}
	if err := d.jobTimeout(starting); err != nil {
		return nil, err
	}
	d.cancelActive()

	var failed, anyCancelled bool
	for _, step := range d.job.Spec.Steps {
		inst, exists := d.byID[step.ID]
		if exists && !inst.Status.Terminal() {
			return d.attend(step, inst)
		}
		if !exists {
			status, waiting, err := d.begin(step, failed)
			if err != nil {
				return nil, err
			}
			if waiting {
				return d.acts, nil
			}
			inst = simulated(step, status)
		}
		d.steps[step.ID] = stepValue(inst)
		failed = failed || (inst.Status == StepFailed && !step.ContinueOnError)
		anyCancelled = anyCancelled || inst.Status == StepCancelled
	}
	d.emit(CompleteRun{Status: finalStatus(failed, anyCancelled, d.timedOut), FinishedAt: d.now})
	return d.acts, nil
}

// finalStatus derives the run status once every step is terminal. A run the
// job timeout cancelled is failed if any step failed or was cancelled, and
// succeeded only if every step still succeeded or was skipped by its `when`.
func finalStatus(failed, anyCancelled, timedOut bool) RunStatus {
	switch {
	case timedOut && (failed || anyCancelled):
		return RunFailed
	case anyCancelled:
		return RunCancelled
	case failed:
		return RunFailed
	}
	return RunSucceeded
}

// cancelActive asks for cancellation of in-flight attempts that started
// before the cancel request. Steps created at or after it (an always()
// cleanup, which may be created in the very Decide that records the cancel,
// at the same instant) are left to finish.
func (d *decider) cancelActive() {
	if !d.canceling {
		return
	}
	for _, inst := range d.st.Steps {
		if inst.Status.Terminal() || !inst.CreatedAt.Before(*d.cancelAt) {
			continue
		}
		for _, ex := range inst.Executions {
			if !ex.State.Terminal() && !ex.CancelRequested {
				d.emit(CancelExecution{Path: inst.Path, ExecutionID: ex.ID, AttemptID: ex.AttemptID})
			}
		}
	}
}

// begin decides what happens to a step that has no instance yet. It returns
// the terminal status when the step is settled without running (skipped,
// cancelled, or failed before start), or waiting=true once it has been
// scheduled.
func (d *decider) begin(step Step, failed bool) (status StepStatus, waiting bool, err error) {
	vars := d.vars(failed)
	when := step.When
	if when == "" {
		when = defaultWhen
	}
	ok, werr := d.e.env.EvalBool(when, vars)
	if werr != nil {
		return d.fail(step, fmt.Errorf("evaluating when: %w", werr)), false, nil
	}
	if !ok {
		return d.skip(step, failed), false, nil
	}
	input, ierr := d.e.ResolveInput(step, vars)
	if ierr != nil {
		return d.fail(step, fmt.Errorf("resolving input: %w", ierr)), false, nil
	}
	timeout, err := parseTimeout(step)
	if err != nil {
		return "", false, err
	}
	d.emit(
		CreateStepInstance{
			Path: step.ID, StepID: step.ID, Plugin: step.Plugin, Input: input,
			ContinueOnError: step.ContinueOnError, RunsOn: step.RunsOn, CreatedAt: d.now,
		},
		ScheduleExecution{Path: step.ID, Attempt: 1, ScheduledAt: d.now, Timeout: timeout},
	)
	return StepPending, true, nil
}

func (d *decider) skip(step Step, failed bool) StepStatus {
	status, reason := StepSkipped, "when condition is false"
	switch {
	case d.timedOut:
		status, reason = StepCancelled, "job timed out"
	case d.canceling:
		status, reason = StepCancelled, "run cancelled"
	case failed:
		reason = "a previous step failed"
	}
	d.emit(SkipStep{Path: step.ID, StepID: step.ID, Status: status, Reason: reason})
	return status
}

func (d *decider) fail(step Step, err error) StepStatus {
	d.emit(FailStep{
		Path: step.ID, StepID: step.ID, ContinueOnError: step.ContinueOnError,
		Error: StepError{Kind: ErrKindExpression, Message: err.Error()},
	})
	return StepFailed
}

func parseTimeout(step Step) (Duration, error) {
	if step.Timeout == "" {
		return 0, nil
	}
	t, err := time.ParseDuration(step.Timeout)
	if err != nil {
		return 0, fmt.Errorf("step %q: invalid timeout %q: %w", step.ID, step.Timeout, err)
	}
	return Duration(t), nil
}

// jobTimeout applies spec.timeout, which covers the run from its start (time
// spent queued does not count). When the run starts it asks for a wake at the
// deadline; once the deadline has passed and no cancel was requested yet it
// emits TimeoutRun and treats the run as canceling from now on, so the
// ordinary cancel rules decide the rest in this same call. The cancel is
// recorded by the store (sticky), so controllers with skewed clocks cannot
// disagree about it afterwards, and a user cancel that commits first wins.
func (d *decider) jobTimeout(starting bool) error {
	if d.canceling || d.runStatus.Terminal() {
		return nil
	}
	if d.job.Spec.Timeout != "" {
		if _, err := time.ParseDuration(d.job.Spec.Timeout); err != nil {
			return fmt.Errorf("invalid job timeout %q: %w", d.job.Spec.Timeout, err)
		}
	}
	deadline, ok := d.jobDeadline()
	if !ok {
		return nil
	}
	if starting {
		d.emit(ScheduleWake{At: deadline})
	}
	if d.now.Before(deadline) {
		return nil
	}
	at := d.now
	d.emit(TimeoutRun{At: at})
	d.canceling, d.timedOut, d.cancelAt = true, true, &at
	return nil
}
