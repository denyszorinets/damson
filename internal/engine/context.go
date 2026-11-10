package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/expr"
)

// MaxResolvedInputBytes caps the resolved input persisted on a StepInstance.
const MaxResolvedInputBytes = 1 << 20

// vars builds the expression context for the next step. It contains only
// plain data from the run state: never secret values (§5.4).
func (d *decider) vars(sc *scope, failed bool) expr.Vars {
	return expr.Vars{
		Inputs: d.st.Inputs,
		Run: map[string]any{
			"id":           d.st.ID,
			"status":       string(d.runStatus),
			"trigger":      d.st.Trigger,
			"triggered_by": d.st.TriggeredBy,
			"started_at":   formatTime(&d.startedAt),
		},
		Job: map[string]any{
			"name":    d.st.Job.Name,
			"project": d.st.Job.Project,
			"version": d.st.Job.Version,
		},
		Steps:   sc.stepsVar(),
		Error:   sc.errorVar(),
		Node:    sc.nodeVar(),
		Item:    sc.itemVar(),
		HasItem: sc.inLane(),
		Targets: targetList(d.st.Targets),
		Status: expr.Status{
			Success:   !failed && (!d.canceling || sc.shielded) && !sc.anyCancelled(),
			Failure:   failed,
			Cancelled: d.canceling,
		},
	}
}

// stepValue is the `steps.<id>` expression value of an instance.
func stepValue(inst StepInstanceState) map[string]any {
	outputs := inst.Outputs
	if outputs == nil {
		outputs = map[string]any{}
	}
	var errv any
	if inst.Error != nil {
		m := map[string]any{"kind": inst.Error.Kind, "message": inst.Error.Message}
		if inst.Error.ExitCode != nil {
			m["exit_code"] = float64(*inst.Error.ExitCode)
		}
		errv = m
	}
	return map[string]any{
		"status":      string(inst.Status),
		"outputs":     outputs,
		"error":       errv,
		"started_at":  formatTime(inst.StartedAt),
		"finished_at": formatTime(inst.FinishedAt),
	}
}

// value is the `steps.<id>` expression value of the step at path. A group's
// value also carries `handled` and `steps`, the values of its body steps; a
// for_each group's carries `lanes` instead of `steps`.
func (d *decider) value(path string, step Step) map[string]any {
	inst := d.byPath[path]
	v := stepValue(inst)
	if !step.IsGroup() {
		return v
	}
	v["handled"] = inst.Handled
	if step.ForEach != "" {
		v["lanes"] = d.laneValues(path, step)
		return v
	}
	v["steps"] = d.childValues(path+"/", step.Steps)
	return v
}

// childValues is the map of `steps` values of the steps of a list that already
// have an instance, under the given path prefix.
func (d *decider) childValues(prefix string, list []Step) map[string]any {
	out := map[string]any{}
	for _, c := range list {
		if _, ok := d.byPath[ChildPath(prefix, c.ID)]; ok {
			out[c.ID] = d.value(ChildPath(prefix, c.ID), c)
		}
	}
	return out
}

func formatTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// ResolveInput interpolates the ${{ }} expressions in step.With and returns
// the resolved plugin input as JSON. A string that is exactly one expression
// keeps the expression's type; anything else is concatenated as a string
// (§5.4). The input is not modified. The result carries no secret values:
// secret:// references stay literal strings.
func (e *Engine) ResolveInput(step Step, vars expr.Vars) (json.RawMessage, error) {
	with := any(map[string]any{})
	if step.With != nil {
		with = step.With
	}
	out, err := e.env.Resolve(with, vars)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, fmt.Errorf("encoding resolved input: %w", err)
	}
	b := bytes.TrimRight(buf.Bytes(), "\n")
	if len(b) > MaxResolvedInputBytes {
		return nil, fmt.Errorf("%w: resolved input is %d bytes, limit %d", expr.ErrResultSize, len(b), MaxResolvedInputBytes)
	}
	return b, nil
}

// targetList is the `targets` expression variable.
func targetList(nodes []map[string]any) []any {
	out := make([]any, len(nodes))
	for i, n := range nodes {
		out[i] = n
	}
	return out
}
