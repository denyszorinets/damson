package engine

import (
	"encoding/json"
	"fmt"
	"strings"
)

// A scope is one step list being walked: the top-level list of the job, or the
// body of a group. Its prefix is the path prefix of the instances it holds,
// vis the `steps` values of the earlier siblings in this list, and parent the
// enclosing scope. Expressions see their own scope's earlier siblings and,
// further out, those of every enclosing list (the inner ones shadow).
type scope struct {
	prefix string
	list   []Step
	parent *scope
	vis    map[string]any
	// shielded marks the body of a group that was created at or after the
	// cancel (a group admitted by always() or cancelled() once the run is being
	// cancelled): the run-level cancel does not count against success() there,
	// so its steps run like the always() steps created after a cancel do.
	shielded bool
	// finally marks a finally list and everything nested in it: its steps are
	// protected from a cancel until the finally grace ends.
	finally bool
	// cancelled is true once a step of this list was cancelled (success() is
	// then false for the steps after it).
	cancelled bool
	// errv is the `error` bound in a catch list; nested scopes inherit it.
	errv map[string]any
	// item is the for_each item bound in a lane scope (hasItem tells a nil item
	// from no binding); nested scopes inherit it.
	item    any
	hasItem bool
}

func newScope(prefix string, list []Step, parent *scope) *scope {
	return &scope{prefix: prefix, list: list, parent: parent, vis: map[string]any{}}
}

// stepsVar is the `steps` expression variable for a step of this scope: a
// fresh map, outer scopes first so an inner id shadows an outer one.
func (sc *scope) stepsVar() map[string]any {
	var chain []*scope
	for s := sc; s != nil; s = s.parent {
		chain = append(chain, s)
	}
	out := map[string]any{}
	for i := len(chain) - 1; i >= 0; i-- {
		for k, v := range chain[i].vis {
			out[k] = v
		}
	}
	return out
}

// anyCancelled reports whether a step already walked in this list was
// cancelled (success() is then false).
func (sc *scope) anyCancelled() bool { return sc.cancelled }

// errorVar is the `error` expression variable: the one bound by the nearest
// enclosing catch list, if any.
func (sc *scope) errorVar() map[string]any {
	for s := sc; s != nil; s = s.parent {
		if s.errv != nil {
			return s.errv
		}
	}
	return nil
}

// outcome is what walking a scope leaves behind. done is false while some
// step still has work in flight (the actions to start it are already emitted);
// failed is true when a step failed and did not continue_on_error, with err
// the first such failure; cancelled when a step was cancelled.
type outcome struct {
	done, failed, cancelled bool
	err                     *StepError
}

// walk goes through the list in order (rule 1). A step that is not finished
// ends the walk: nothing after it starts. A finished step updates the scope's
// failed and cancelled state, which the `when` of the steps after it sees
// through the status functions (rule 3).
func (d *decider) walk(sc *scope) (outcome, error) {
	o := outcome{done: true}
	for _, step := range sc.list {
		path := ChildPath(sc.prefix, step.ID)
		inst, exists := d.byPath[path]
		switch {
		case exists && !inst.Status.Terminal():
			if step.IsGroup() {
				settled, err := d.runGroupOrLanes(sc, step, path)
				if err != nil || !settled {
					return outcome{}, err
				}
			} else if err := d.attend(sc, step, inst); err != nil {
				return outcome{}, err
			} else {
				return outcome{}, nil
			}
		case !exists:
			var settled bool
			var err error
			if step.IsGroup() {
				settled, err = d.beginGroup(sc, step, path, o.failed)
			} else {
				settled, err = d.begin(sc, step, path, o.failed)
			}
			if err != nil || !settled {
				return outcome{}, err
			}
		}
		inst = d.byPath[path]
		sc.vis[step.ID] = d.value(path, step)
		if inst.Status == StepFailed && !step.ContinueOnError {
			if !o.failed {
				o.err = failureOf(inst, path)
			}
			o.failed = true
		}
		o.cancelled = o.cancelled || inst.Status == StepCancelled
		sc.cancelled = o.cancelled
	}
	return o, nil
}

// failureOf is the error that stopped a step, with the path of the plugin step
// that raised it (kept when it comes up from a nested group).
func failureOf(inst StepInstanceState, path string) *StepError {
	e := StepError{Kind: "error", Message: fmt.Sprintf("step %s failed", path)}
	if inst.Error != nil {
		e = *inst.Error
	}
	if e.Path == "" {
		e.Path = path
	}
	return &e
}

// beginGroup decides what happens to a group that has no instance yet. Its
// `when` is evaluated in the enclosing scope like a plugin step's. A group that
// is skipped or fails to evaluate gets no children (it never started). Otherwise
// the instance is created and the walk continues into its body in this same
// Decide; settled reports whether the group is already finished.
func (d *decider) beginGroup(sc *scope, step Step, path string, failed bool) (settled bool, err error) {
	if sc.finally && d.graceOver() {
		d.skipGraceExpired(step, path, "group")
		return true, nil
	}
	ok, werr := d.evalWhen(sc, step, failed)
	if werr != nil {
		d.fail(step, path, "group", fmt.Errorf("evaluating when: %w", werr))
		return true, nil
	}
	if !ok {
		d.skip(sc, step, path, "group", failed)
		return true, nil
	}
	started := d.now
	create := CreateStepInstance{Path: path, StepID: step.ID, Kind: kindGroup, CreatedAt: d.now, ContinueOnError: step.ContinueOnError}
	inst := StepInstanceState{Path: path, StepID: step.ID, Kind: kindGroup, Status: StepPending, CreatedAt: d.now, StartedAt: &started}
	if step.ForEach != "" {
		// The list is evaluated once, here, and frozen on the group's row.
		lanes, lerr := d.evalLanes(sc, step, failed)
		var input json.RawMessage
		if lerr == nil {
			input, lerr = lanesInput(lanes)
		}
		if lerr != nil {
			d.fail(step, path, "group", fmt.Errorf("evaluating for_each: %w", lerr))
			return true, nil
		}
		create.Input, inst.Lanes = input, lanes
		if inst.Lanes == nil {
			inst.Lanes = []Lane{}
		}
	}
	d.emit(create)
	d.byPath[path] = inst
	return d.runGroupOrLanes(sc, step, path)
}

// runGroupOrLanes advances a started group: a for_each group runs its lanes,
// any other group its body, catch and finally.
func (d *decider) runGroupOrLanes(sc *scope, step Step, path string) (bool, error) {
	if step.ForEach != "" {
		return d.runLanes(sc, step, path)
	}
	return d.runGroup(sc, step, path)
}

// runGroup walks a group whose instance exists: the body first; then, if a
// body step failed and the group has a catch list, the catch list with `error`
// bound to that failure; then the finally list, which always runs. When all of
// them are done the group is finished (groupStatus).
func (d *decider) runGroup(sc *scope, step Step, path string) (settled bool, err error) {
	body := newScope(path+"/", step.Steps, sc)
	body.shielded = sc.shielded || d.createdAfterCancel(path)
	body.finally = sc.finally
	ob, err := d.walk(body)
	if err != nil || !ob.done {
		return false, err
	}

	caught := ob.failed && len(step.Catch) > 0
	oc := outcome{done: true}
	var catchVis map[string]any
	if caught {
		cs := newScope(path+"/"+SegCatch+"/", step.Catch, sc)
		cs.shielded, cs.finally = body.shielded, sc.finally
		cs.seed(body.vis)
		cs.errv = errorValue(ob.err)
		if oc, err = d.walk(cs); err != nil || !oc.done {
			return false, err
		}
		catchVis = cs.vis
	}

	of := outcome{done: true}
	if len(step.Finally) > 0 {
		fs := newScope(path+"/"+SegFinally+"/", step.Finally, sc)
		// The list is cut at the grace only when its group predates the cancel.
		fs.shielded, fs.finally = true, sc.finally || !d.createdAfterCancel(path)
		fs.seed(body.vis)
		fs.seed(catchVis)
		if of, err = d.walk(fs); err != nil || !of.done {
			return false, err
		}
	}

	status, gerr, handled := groupStatus(step, ob, caught, oc, of)
	d.emit(CompleteGroup{Path: path, Status: status, Handled: handled, Error: gerr})
	prev := d.byPath[path]
	finished := d.now
	prev.Status, prev.Error, prev.Handled, prev.FinishedAt = status, gerr, handled, &finished
	d.byPath[path] = prev
	return true, nil
}

// groupStatus derives a finished group's status and the error that goes with
// it. The first rule that matches wins (ARCHITECTURE 5.3 rules 4 and 5):
//
//  1. a failed finally list fails the group with its error;
//  2. a failed catch list fails it with the catch error, and a cancelled one
//     leaves the body error standing;
//  3. a caught failure with rethrow fails it with the original body error;
//  4. a caught failure whose catch list did not fail is handled: the group
//     succeeds and keeps the body error (a catch whose steps were all skipped
//     counts as handled);
//  5. an uncaught body failure fails it with that error;
//  6. a cancelled step anywhere cancels it;
//  7. otherwise it succeeded.
//
// At group level a real failure beats a cancel, so the failure() handlers of
// the enclosing list still see it during a cancel.
func groupStatus(step Step, ob outcome, caught bool, oc, of outcome) (status StepStatus, err *StepError, handled bool) {
	switch {
	case of.failed:
		return StepFailed, of.err, false
	case caught && oc.failed:
		return StepFailed, oc.err, false
	case caught && oc.cancelled:
		return StepFailed, ob.err, false
	case caught && step.Rethrow != nil && *step.Rethrow:
		return StepFailed, ob.err, false
	case caught:
		return StepSucceeded, ob.err, true
	case ob.failed:
		return StepFailed, ob.err, false
	case ob.cancelled || oc.cancelled || of.cancelled:
		return StepCancelled, nil, false
	}
	return StepSucceeded, nil, false
}

// seed copies the values of earlier lists of the same group into this list's
// visible steps, so catch and finally see the body (and finally the catch).
func (sc *scope) seed(vis map[string]any) {
	for k, v := range vis {
		sc.vis[k] = v
	}
}

// errorValue is the `error` variable bound in a catch list: the failing step's
// path and last segment, kind, message and, when known, exit code.
func errorValue(e *StepError) map[string]any {
	if e == nil {
		return map[string]any{}
	}
	step := e.Path
	if i := strings.LastIndex(step, "/"); i >= 0 {
		step = step[i+1:]
	}
	m := map[string]any{"step": step, "path": e.Path, "kind": e.Kind, "message": e.Message}
	if e.ExitCode != nil {
		m["exit_code"] = float64(*e.ExitCode)
	}
	return m
}

// createdAfterCancel reports whether the instance at path was created at or
// after the cancel request (the same instant counts, as for steps).
func (d *decider) createdAfterCancel(path string) bool {
	inst, ok := d.byPath[path]
	return d.canceling && ok && !inst.CreatedAt.Before(*d.cancelAt)
}
