package advance

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

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
		if n.Kind != store.KindStep {
			continue // groups arrive with M3
		}
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
		Path: n.Path, StepID: n.Path[strings.LastIndex(n.Path, "/")+1:], Status: engine.StepStatusFromStore(string(n.Status)),
		CreatedAt: n.CreatedAt, StartedAt: n.StartedAt, FinishedAt: n.FinishedAt,
	}
	if err := decodeObject(n.Outputs, &inst.Outputs); err != nil {
		return inst, fmt.Errorf("decoding outputs of step %s: %w", n.Path, err)
	}
	if n.Status == store.StepFailed {
		inst.Error = decodeStepError(n.Error)
	}
	for _, ex := range n.Executions {
		inst.Executions = append(inst.Executions, engine.ExecutionView{ID: ex.ID, AttemptID: ex.AttemptID, State: engine.ExecStateFromStore(string(ex.State))})
	}
	return inst, nil
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
