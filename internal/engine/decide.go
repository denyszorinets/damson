package engine

import (
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/expr"
)

// defaultWhen is the condition of a step without `when` (§5.2).
const defaultWhen = "success()"

// Engine evaluates the control-flow rules of §5.3. It holds only the
// compiled-expression environment, so it is safe for concurrent use and
// Decide stays a pure function of its arguments.
type Engine struct {
	env *expr.Env
}

// New builds an Engine whose expressions run under the given limits.
func New(limits expr.Limits) (*Engine, error) {
	env, err := expr.NewEnv(limits)
	if err != nil {
		return nil, err
	}
	return &Engine{env: env}, nil
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
	d := &decider{e: e, job: job, st: st, now: now, cancelling: st.CancelRequestedAt != nil}
	if err := d.index(); err != nil {
		return nil, err
	}
	return d.decide()
}

type decider struct {
	e          *Engine
	job        *Job
	st         RunState
	now        time.Time
	cancelling bool

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

func (d *decider) decide() ([]Action, error) {
	d.runStatus = d.st.Status
	d.startedAt = d.now
	if d.st.StartedAt != nil {
		d.startedAt = *d.st.StartedAt
	}
	if d.runStatus == RunQueued {
		d.emit(StartRun{StartedAt: d.now})
		d.runStatus = RunRunning
	}
	d.cancelActive()

	var failed, anyCancelled bool
	for _, step := range d.job.Spec.Steps {
		inst, exists := d.byID[step.ID]
		if exists && !inst.Status.Terminal() {
			return d.waitOn(step, inst)
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
	d.emit(CompleteRun{Status: finalStatus(failed, anyCancelled), FinishedAt: d.now})
	return d.acts, nil
}

// finalStatus derives the run status once every step is terminal.
func finalStatus(failed, anyCancelled bool) RunStatus {
	switch {
	case anyCancelled:
		return RunCancelled
	case failed:
		return RunFailed
	}
	return RunSucceeded
}

// cancelActive asks for cancellation of in-flight attempts that started
// before the cancel request. Steps created after it (an always() cleanup) are
// left to finish.
func (d *decider) cancelActive() {
	if !d.cancelling {
		return
	}
	for _, inst := range d.st.Steps {
		if inst.Status.Terminal() || inst.CreatedAt.After(*d.st.CancelRequestedAt) {
			continue
		}
		for _, ex := range inst.Executions {
			if !ex.State.Terminal() && !ex.CancelRequested {
				d.emit(CancelExecution{Path: inst.Path, ExecutionID: ex.ID, AttemptID: ex.AttemptID})
			}
		}
	}
}

// waitOn handles a step instance that is not terminal yet. Normally an
// execution is in flight and there is nothing to do. An instance with no
// execution at all (it cannot happen when actions are applied atomically) is
// repaired by scheduling its first one.
func (d *decider) waitOn(step Step, inst StepInstanceState) ([]Action, error) {
	if len(inst.Executions) == 0 {
		timeout, err := parseTimeout(step)
		if err != nil {
			return nil, err
		}
		d.emit(ScheduleExecution{Path: inst.Path, Attempt: 1, ScheduledAt: d.now, Timeout: timeout})
	}
	return d.acts, nil
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
	case d.cancelling:
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
