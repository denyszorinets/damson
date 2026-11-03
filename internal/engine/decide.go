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

	// byPath indexes the step instances by path. The walk adds the state a
	// step will have once this Decide's actions apply (a skipped step, a
	// finished group), so the steps after it see it.
	byPath    map[string]StepInstanceState
	runStatus RunStatus
	startedAt time.Time
	acts      []Action
}

// kindGroup is StepInstanceState.Kind of a group.
const kindGroup = "group"

func (d *decider) index() error {
	d.byPath = make(map[string]StepInstanceState, len(d.st.Steps))
	for _, inst := range d.st.Steps {
		spec, ok := d.job.StepAt(inst.Path)
		if !ok {
			return fmt.Errorf("run %s: step instance %q has no step in the job spec", d.st.ID, inst.Path)
		}
		if _, dup := d.byPath[inst.Path]; dup {
			return fmt.Errorf("run %s: duplicate step instance for path %q", d.st.ID, inst.Path)
		}
		if spec.IsGroup() != (inst.Kind == kindGroup) {
			return fmt.Errorf("run %s: step instance %q is a %s but the job spec says %s", d.st.ID, inst.Path, kindName(inst.Kind), kindName(map[bool]string{true: kindGroup, false: ""}[spec.IsGroup()]))
		}
		d.byPath[inst.Path] = inst
	}
	return nil
}

func kindName(k string) string {
	if k == kindGroup {
		return "group"
	}
	return "step"
}

func (d *decider) emit(a ...Action) { d.acts = append(d.acts, a...) }

// decide walks the job's step list (rules 1-3, 8) with the start and timeout
// bookkeeping around it, and completes the run when the walk is done.
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

	o, err := d.walk(newScope("", d.job.Spec.Steps, nil))
	if err != nil {
		return nil, err
	}
	if o.done {
		d.emit(CompleteRun{Status: finalStatus(o.failed, o.cancelled, d.timedOut), FinishedAt: d.now})
	}
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

// evalWhen evaluates a step's `when` (default success()) in its scope.
func (d *decider) evalWhen(sc *scope, step Step, failed bool) (bool, error) {
	when := step.When
	if when == "" {
		when = defaultWhen
	}
	return d.e.env.EvalBool(when, d.vars(sc, failed))
}

// begin decides what happens to a plugin step that has no instance yet. It
// returns settled=true when the step is finished without running (skipped,
// cancelled, or failed before start), and settled=false once its first
// execution has been scheduled.
func (d *decider) begin(sc *scope, step Step, path string, failed bool) (settled bool, err error) {
	ok, werr := d.evalWhen(sc, step, failed)
	if werr != nil {
		d.fail(step, path, "", fmt.Errorf("evaluating when: %w", werr))
		return true, nil
	}
	if !ok {
		d.skip(sc, step, path, "", failed)
		return true, nil
	}
	input, ierr := d.e.ResolveInput(step, d.vars(sc, failed))
	if ierr != nil {
		d.fail(step, path, "", fmt.Errorf("resolving input: %w", ierr))
		return true, nil
	}
	timeout, err := parseTimeout(step)
	if err != nil {
		return false, err
	}
	d.emit(
		CreateStepInstance{
			Path: path, StepID: step.ID, Plugin: step.Plugin, Input: input,
			ContinueOnError: step.ContinueOnError, RunsOn: step.RunsOn, CreatedAt: d.now,
		},
		ScheduleExecution{Path: path, Attempt: 1, ScheduledAt: d.now, Timeout: timeout},
	)
	d.byPath[path] = StepInstanceState{Path: path, StepID: step.ID, Status: StepPending, CreatedAt: d.now}
	return false, nil
}

func (d *decider) skip(sc *scope, step Step, path, kind string, failed bool) {
	status, reason := StepSkipped, "when condition is false"
	switch {
	case d.timedOut && !sc.shielded:
		status, reason = StepCancelled, "job timed out"
	case d.canceling && !sc.shielded:
		status, reason = StepCancelled, "run cancelled"
	case failed:
		reason = "a previous step failed"
	}
	d.emit(SkipStep{Path: path, StepID: step.ID, Kind: kind, Status: status, Reason: reason})
	d.byPath[path] = StepInstanceState{Path: path, StepID: step.ID, Kind: kind, Status: status, CreatedAt: d.now}
}

func (d *decider) fail(step Step, path, kind string, err error) {
	se := StepError{Kind: ErrKindExpression, Message: err.Error()}
	d.emit(FailStep{Path: path, StepID: step.ID, Kind: kind, ContinueOnError: step.ContinueOnError, Error: se})
	d.byPath[path] = StepInstanceState{Path: path, StepID: step.ID, Kind: kind, Status: StepFailed, Error: &se, CreatedAt: d.now}
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
