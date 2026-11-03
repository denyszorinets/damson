package engine

import "fmt"

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
func (sc *scope) anyCancelled() bool {
	for _, v := range sc.vis {
		if m, ok := v.(map[string]any); ok && m["status"] == string(StepCancelled) {
			return true
		}
	}
	return false
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
				settled, err := d.runGroup(sc, step, path)
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
	ok, werr := d.evalWhen(sc, step, failed)
	if werr != nil {
		d.fail(step, path, "group", fmt.Errorf("evaluating when: %w", werr))
		return true, nil
	}
	if !ok {
		d.skip(step, path, "group", failed)
		return true, nil
	}
	d.emit(CreateStepInstance{Path: path, StepID: step.ID, Kind: kindGroup, CreatedAt: d.now, ContinueOnError: step.ContinueOnError})
	d.byPath[path] = StepInstanceState{Path: path, StepID: step.ID, Kind: kindGroup, Status: StepPending, CreatedAt: d.now}
	return d.runGroup(sc, step, path)
}

// runGroup walks the body of a group whose instance exists and, once the body
// is done, finishes the group: failed if a body step failed (and did not
// continue_on_error), else cancelled if any was cancelled, else succeeded.
func (d *decider) runGroup(sc *scope, step Step, path string) (settled bool, err error) {
	body := newScope(path+"/", step.Steps, sc)
	o, err := d.walk(body)
	if err != nil || !o.done {
		return false, err
	}
	status, gerr := StepSucceeded, (*StepError)(nil)
	switch {
	case o.failed:
		status, gerr = StepFailed, o.err
	case o.cancelled:
		status = StepCancelled
	}
	d.emit(CompleteGroup{Path: path, Status: status, Error: gerr})
	prev := d.byPath[path]
	prev.Status, prev.Error = status, gerr
	d.byPath[path] = prev
	return true, nil
}
