package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// Limits of a for_each list (ADR-0038).
const (
	maxLanes = 1000
)

// laneKeyPattern is the grammar of a lane key: it appears in step paths, so it
// excludes '/', '[' and ']'.
var laneKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// laneKeyOf derives the key of the item at index i: the `name` of an object
// that has a string one, the item itself when it is a string, otherwise its
// index.
func laneKeyOf(item any, i int) string {
	switch t := item.(type) {
	case map[string]any:
		if name, ok := t["name"].(string); ok {
			return name
		}
	case string:
		return t
	}
	return strconv.Itoa(i)
}

// evalLanes evaluates the for_each expression of a group in its enclosing
// scope and derives the lane keys. The result is frozen on the group's row, so
// it is evaluated once per run. An error (not a list, too many items, an
// invalid or duplicate key) fails the group before any lane starts.
func (d *decider) evalLanes(sc *scope, step Step, failed bool) ([]Lane, error) {
	items, err := d.e.env.EvalList(step.ForEach, d.vars(sc, failed))
	if err != nil {
		return nil, err
	}
	if len(items) > maxLanes {
		return nil, fmt.Errorf("for_each has %d items, the limit is %d", len(items), maxLanes)
	}
	lanes := make([]Lane, len(items))
	seen := make(map[string]int, len(items))
	for i, item := range items {
		key := laneKeyOf(item, i)
		if !laneKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("item %d has the lane key %q, which must match %s", i, key, laneKeyPattern)
		}
		if first, dup := seen[key]; dup {
			return nil, fmt.Errorf("items %d and %d have the same lane key %q", first, i, key)
		}
		seen[key] = i
		lanes[i] = Lane{Key: key, Item: item}
	}
	return lanes, nil
}

// lanesInput is the group row's input: the frozen lane list.
func lanesInput(lanes []Lane) (json.RawMessage, error) {
	if lanes == nil {
		lanes = []Lane{}
	}
	return json.Marshal(struct {
		Lanes []Lane `json:"lanes"`
	}{lanes})
}

// bindLane makes the scope a lane's: it binds `item` (and `node`, the item when
// it is an object) for everything evaluated in it and below.
func (sc *scope) bindLane(item any) {
	sc.item, sc.hasItem = item, true
}

// itemVar is the `item` of the nearest enclosing lane, nil outside a lane.
func (sc *scope) itemVar() any {
	for s := sc; s != nil; s = s.parent {
		if s.hasItem {
			return s.item
		}
	}
	return nil
}

// nodeVar is the `node` variable: the item of the nearest enclosing lane when
// it is an object (a node of `targets`), otherwise empty.
func (sc *scope) nodeVar() map[string]any {
	if m, ok := sc.itemVar().(map[string]any); ok {
		return m
	}
	return nil
}

// runLanes advances a for_each group whose row exists: it opens lanes in list
// order up to max_parallel, finishes the ones that can finish, and, once every
// lane is terminal, finishes the group itself. Unlike a step list the walk does
// not stop at the first unfinished lane: every open lane progresses in this
// Decide.
//
// The lanes that exist are always a prefix of the list (they are created in
// order). After fail_threshold lanes have failed, the lanes that have not
// started are cancelled; the running ones finish. During a cancel or the finally
// grace's end the lanes that have not started are cancelled too, unless the
// group itself was admitted after the cancel.
func (d *decider) runLanes(sc *scope, step Step, path string) (settled bool, err error) {
	lanes := d.byPath[path].Lanes
	limit := len(lanes)
	if step.MaxParallel != nil && *step.MaxParallel < limit {
		limit = *step.MaxParallel
	}
	shielded := sc.shielded || d.createdAfterCancel(path)
	settlesBefore := d.settles

	var open, failed int
	var firstErr *StepError
	anyCancelled, allDone := false, true
	for _, ln := range lanes {
		lp := LanePath(path, ln.Key)
		bind := newScope(lp+"/", nil, sc)
		bind.bindLane(ln.Item)
		bind.shielded, bind.finally = shielded, sc.finally

		inst, exists := d.byPath[lp]
		switch {
		case exists && !inst.Status.Terminal():
			done, err := d.runGroup(bind, step, lp)
			if err != nil {
				return false, err
			}
			if !done {
				open++
			}
		case !exists:
			if reason := d.laneStop(sc, shielded, failed, step); reason != "" {
				d.skipLane(step, lp, reason)
			} else if open >= limit || d.settles > settlesBefore {
				// Waits: all slots are taken, or a lane that just failed is still
				// to be settled and the threshold must see it before another opens.
				allDone = false
				continue
			} else {
				d.emit(CreateStepInstance{Path: lp, StepID: step.ID, Kind: kindGroup, CreatedAt: d.now})
				started := d.now
				d.byPath[lp] = StepInstanceState{Path: lp, StepID: step.ID, Kind: kindGroup, Status: StepPending, CreatedAt: d.now, StartedAt: &started}
				done, err := d.runGroup(bind, step, lp)
				if err != nil {
					return false, err
				}
				if !done {
					open++
				}
			}
		}
		li := d.byPath[lp]
		if !li.Status.Terminal() {
			allDone = false
			continue
		}
		if li.Status == StepFailed {
			if failed == 0 {
				firstErr = failureOf(li, lp)
			}
			failed++
		}
		anyCancelled = anyCancelled || li.Status == StepCancelled
	}
	if !allDone {
		return false, nil
	}

	status, gerr := StepSucceeded, (*StepError)(nil)
	switch {
	case failed > 0:
		status, gerr = StepFailed, firstErr
	case anyCancelled:
		status = StepCancelled
	}
	d.emit(CompleteGroup{Path: path, Status: status, Error: gerr})
	prev := d.byPath[path]
	finished := d.now
	prev.Status, prev.Error, prev.FinishedAt = status, gerr, &finished
	d.byPath[path] = prev
	return true, nil
}

// laneStop is why a lane that has not started must not start, or "".
func (d *decider) laneStop(sc *scope, shielded bool, failed int, step Step) string {
	switch {
	case step.FailThreshold != nil && failed >= *step.FailThreshold:
		return "fail_threshold reached"
	case sc.finally && d.graceOver():
		return "finally grace expired"
	case d.timedOut && !shielded:
		return "job timed out"
	case d.canceling && !shielded:
		return "run cancelled"
	}
	return ""
}

// skipLane cancels a lane that never started: its row is created cancelled, with
// no steps.
func (d *decider) skipLane(step Step, lp, reason string) {
	d.emit(SkipStep{Path: lp, StepID: step.ID, Kind: kindGroup, Status: StepCancelled, Reason: reason})
	d.byPath[lp] = StepInstanceState{Path: lp, StepID: step.ID, Kind: kindGroup, Status: StepCancelled, CreatedAt: d.now}
}

// laneValues is `steps.<group>.lanes`: one entry per lane that has a row,
// keyed by lane key. A lane's `steps` are the values of its own steps.
func (d *decider) laneValues(path string, step Step) map[string]any {
	out := map[string]any{}
	for _, ln := range d.byPath[path].Lanes {
		lp := LanePath(path, ln.Key)
		inst, ok := d.byPath[lp]
		if !ok {
			continue
		}
		v := stepValue(inst)
		v["key"], v["item"], v["handled"] = ln.Key, ln.Item, inst.Handled
		v["steps"] = d.childValues(lp+"/", step.Steps)
		out[ln.Key] = v
	}
	return out
}
