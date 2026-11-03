package advance

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/store"
)

// jobMeta is the job identity the engine exposes to expressions.
type jobMeta struct {
	Name, Project string
	Version       int
}

// buildState maps the store's view of a run to the engine's RunState. The
// vocabularies differ on purpose: the store has "pending", "canceled" and a
// "running" step status, the engine "queued", "cancelled" and a single
// "pending" step status (queued, dispatched and running are execution states).
func buildState(tree store.RunTree, meta jobMeta) (engine.RunState, error) {
	st := engine.RunState{
		ID:                tree.ID,
		Status:            engine.RunStatusFromStore(string(tree.Status)),
		Job:               engine.JobInfo{Name: meta.Name, Project: meta.Project, Version: strconv.Itoa(meta.Version)},
		StartedAt:         tree.StartedAt,
		CancelRequestedAt: tree.CancelRequestedAt,
		CancelReason:      tree.CancelReason,
	}
	var trigger struct {
		Type string `json:"type"`
		By   string `json:"triggered_by"`
	}
	if err := decodeObject(tree.Trigger, &trigger); err != nil {
		return st, fmt.Errorf("decoding trigger of run %s: %w", tree.ID, err)
	}
	st.Trigger, st.TriggeredBy = trigger.Type, trigger.By
	if err := decodeObject(tree.Options, &st.Inputs); err != nil {
		return st, fmt.Errorf("decoding options of run %s: %w", tree.ID, err)
	}
	for _, n := range tree.Steps {
		inst, err := stepState(n)
		if err != nil {
			return st, err
		}
		st.Steps = append(st.Steps, inst)
	}
	return st, nil
}

func decodeObject(raw json.RawMessage, into any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, into)
}

// stepState maps one step instance. M2 step paths equal their step id, which
// is also the last path segment once M3 prefixes scopes.
func stepState(n store.StepNode) (engine.StepInstanceState, error) {
	inst := engine.StepInstanceState{
		Path: n.Path, StepID: engine.StepIDOf(n.Path), Status: engine.StepStatusFromStore(string(n.Status)),
		CreatedAt: n.CreatedAt, StartedAt: n.StartedAt, FinishedAt: n.FinishedAt,
	}
	if n.Kind == store.KindGroup {
		inst.Kind = "group"
		lanes, err := decodeLanes(n.Input)
		if err != nil {
			return inst, fmt.Errorf("decoding the lanes of group %s: %w", n.Path, err)
		}
		inst.Lanes = lanes
	}
	if err := decodeObject(n.Outputs, &inst.Outputs); err != nil {
		return inst, fmt.Errorf("decoding outputs of step %s: %w", n.Path, err)
	}
	if n.Status == store.StepFailed || n.Handled() {
		inst.Error = decodeStepError(n.Error)
	}
	inst.Handled = n.Handled()
	for _, ex := range n.Executions {
		inst.Executions = append(inst.Executions, executionView(ex))
	}
	return inst, nil
}

// executionView maps one attempt. A finished attempt that did not succeed
// carries its error (the stored kind, or one derived from its state) and the
// time it ended, which is the base of the retry backoff.
func executionView(ex store.Execution) engine.ExecutionView {
	v := engine.ExecutionView{
		ID: ex.ID, AttemptID: ex.AttemptID, Attempt: ex.AttemptNo,
		State: engine.ExecStateFromStore(string(ex.State)), CancelRequested: ex.CancelRequestedAt != nil,
	}
	if ex.State.Terminal() && ex.State != store.ExecSucceeded {
		v.FinishedAt = ex.FinishedAt
		_, msg := store.SplitStepError(ex.Error)
		v.Error = &engine.StepError{Kind: store.ErrorKindOf(ex), Message: msg}
		if full := decodeStepError(ex.Error); full != nil && full.ExitCode != nil {
			v.Error.ExitCode = full.ExitCode
		}
	}
	return v
}

// decodeStepError reads step_instances.error: a JSON engine.StepError when
// the engine wrote it (FailStep), otherwise plain message text.
func decodeStepError(text string) *engine.StepError {
	var se engine.StepError
	if json.Unmarshal([]byte(text), &se) == nil && se.Kind != "" {
		return &se
	}
	return &engine.StepError{Kind: "error", Message: text}
}

// decodeLanes reads the frozen for_each list from a group row's input: nil for
// a group without one (the input is empty or an empty object).
func decodeLanes(raw json.RawMessage) ([]engine.Lane, error) {
	if len(raw) == 0 || string(raw) == "{}" {
		return nil, nil
	}
	var in struct {
		Lanes []engine.Lane `json:"lanes"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	return in.Lanes, nil
}
