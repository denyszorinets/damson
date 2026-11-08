package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
)

const (
	// triggerAPI is run.trigger for runs started through the API.
	triggerAPI = "api"
	// defaultPool is the pool of a step without runs_on.pool.
	defaultPool = "default"
	// policyQueue is the only concurrency policy supported before M3.
	policyQueue = "queue"
)

// triggerRun is ARCHITECTURE 8.3 step 1 for the API trigger: validate the
// options, ask the engine what the initial state needs, and insert the run
// with its first step instances and executions in one store transaction.
// Target resolution is a stub until the inventory work, and the run.advance
// enqueue arrives with the orchestrator (#53): until then a pending run is
// picked up by that component.
func triggerRun(ctx context.Context, d deps, in *triggerRunInput) (*runDetailOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	body := triggerBody{}
	if in.Body != nil {
		body = *in.Body
	}
	if p := body.ConcurrencyPolicy; p != "" && p != policyQueue {
		return nil, apiError(ctx, invalid("unsupported concurrency policy",
			ProblemDetail{Message: fmt.Sprintf("%q is not supported yet; only %q is", p, policyQueue), Location: "body.concurrency_policy"}))
	}
	trig := triggerFor(ctx)
	nr, number, err := d.prepareRun(ctx, in.ID, body.Version, body.Options, trig)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	project, err := d.projectOfJobRow(ctx, in.ID)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	var tree store.RunTree
	err = d.inTx(ctx, func(tx store.Tx) (string, error) {
		var err error
		if tree, err = tx.CreateRun(ctx, nr, d.now()); err != nil {
			return "", err
		}
		return tree.ID, d.auditRunTrigger(ctx, tx, project, nr, tree.ID, trig)
	})
	if err != nil {
		return nil, apiError(ctx, err)
	}
	return &runDetailOutput{Body: toRunDetail(tree, number)}, nil
}

// resolveOptions applies the schema defaults and validates the result against
// spec.options. Messages never carry option values.
func resolveOptions(spec *engine.Job, given map[string]any) (map[string]any, error) {
	opts, err := engine.WithOptionDefaults(spec, given)
	if err != nil {
		return nil, err
	}
	if spec.Spec.Options == nil {
		if len(opts) > 0 {
			return nil, invalid("the job declares no options",
				ProblemDetail{Message: "options are not accepted by this job", Location: "body.options"})
		}
		return opts, nil
	}
	schema, err := json.Marshal(spec.Spec.Options)
	if err != nil {
		return nil, fmt.Errorf("encoding spec.options: %w", err)
	}
	input, err := json.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("encoding options: %w", err)
	}
	// Values under an x-damson-secret option must be secret:// references.
	// Locations only: neither check ever formats a submitted value.
	fields := secretFields(engine.SecretOptionViolations(spec.Spec.Options, opts))
	err = pluginreg.ValidateJSONSchema("options", schema, input)
	var ve pluginreg.ValidationErrors
	switch {
	case err == nil:
	case errors.Is(err, pluginreg.ErrInputTooLarge):
		return nil, invalid("options are too large")
	case errors.As(err, &ve):
		for _, fe := range ve {
			loc := "options." + fe.Path
			if fe.Path == "$" {
				loc = "options"
			}
			if !hasLocation(fields, loc) {
				fields = append(fields, ProblemDetail{Message: fe.Message, Location: loc})
			}
		}
	default:
		return nil, fmt.Errorf("validating options: %w", err)
	}
	if len(fields) > 0 {
		return nil, invalid("the options are invalid", fields...)
	}
	return opts, nil
}

func secretFields(paths []string) []ProblemDetail {
	out := make([]ProblemDetail, len(paths))
	for i, p := range paths {
		out[i] = ProblemDetail{Message: "must be a secret:// reference", Location: p}
	}
	return out
}

func hasLocation(fields []ProblemDetail, loc string) bool {
	for _, f := range fields {
		if f.Location == loc {
			return true
		}
	}
	return false
}

// newRun builds the run insert from what Decide returns for a queued run: the
// step instances it creates and their first executions. The other actions
// (start, skip, fail, complete) are applied by run.advance.
func (d deps) newRun(ctx context.Context, job store.Job, ver store.JobVersion, spec *engine.Job, opts map[string]any, trig Trigger) (store.NewRun, error) {
	id, err := store.NewID()
	if err != nil {
		return store.NewRun{}, err
	}
	trigger, err := json.Marshal(trig)
	if err != nil {
		return store.NewRun{}, fmt.Errorf("encoding trigger: %w", err)
	}
	options, err := json.Marshal(opts)
	if err != nil {
		return store.NewRun{}, fmt.Errorf("encoding options: %w", err)
	}
	now := d.now()
	state := engine.RunState{
		ID: id, Status: engine.RunQueued, Trigger: trig.Type, Inputs: opts,
		Job: engine.JobInfo{Name: spec.Metadata.Name, Project: spec.Metadata.Project, Version: strconv.Itoa(ver.Version)},
	}
	var frozen []store.RunTarget
	if d.targets != nil && engine.UsesTargets(spec) {
		// One more than the limit, so an overflow is seen rather than cut off.
		pick := func(page []map[string]any) []map[string]any { return d.eng.SelectTargets(spec, opts, page) }
		var err error
		if state.Targets, err = d.targets.Targets(ctx, spec.Metadata.Project, pick, store.MaxRunTargets+1); err != nil {
			return store.NewRun{}, fmt.Errorf("listing targets: %w", err)
		}
		if frozen, err = freezeTargets(state.Targets); err != nil {
			return store.NewRun{}, err
		}
	}
	actions, err := d.eng.Decide(spec, state, now)
	if err != nil {
		return store.NewRun{}, fmt.Errorf("deciding the initial state of run %s: %w", id, err)
	}
	if err := d.checkStepInputs(actions); err != nil {
		return store.NewRun{}, err
	}
	return store.NewRun{
		ID: id, JobID: job.ID, JobVersionID: ver.ID, Trigger: trigger, Options: options,
		Steps: initialSteps(actions), Targets: frozen,
	}, nil
}

// freezeTargets turns the selected nodes into the run's frozen target list.
// More than store.MaxRunTargets is refused: the run would not fit its lanes.
func freezeTargets(nodes []map[string]any) ([]store.RunTarget, error) {
	if len(nodes) > store.MaxRunTargets {
		return nil, invalid(fmt.Sprintf("spec.targets matches more than %d nodes; a run takes at most %d", store.MaxRunTargets, store.MaxRunTargets),
			ProblemDetail{Message: "too many nodes", Location: "spec.targets"})
	}
	out := make([]store.RunTarget, 0, len(nodes))
	for _, n := range nodes {
		name, _ := n["name"].(string)
		id, _ := n["id"].(string)
		b, err := json.Marshal(n)
		if err != nil {
			return nil, fmt.Errorf("encoding target %q: %w", name, err)
		}
		out = append(out, store.RunTarget{Name: name, NodeID: id, Node: b})
	}
	return out, nil
}

// checkStepInputs validates the resolved input of each step instance the run
// is created with against the plugin's input schema, when the registry knows
// the plugin. A plugin it does not know (no agent connected yet) is allowed
// to queue: dispatch validates again against the version an agent will run
// (#53). Messages carry schema paths only, never input values.
func (d deps) checkStepInputs(actions []engine.Action) error {
	if d.plugins == nil {
		return nil
	}
	var fields []ProblemDetail
	for _, a := range actions {
		c, ok := a.(engine.CreateStepInstance)
		if !ok {
			continue
		}
		m, err := d.plugins.Lookup(c.Plugin)
		if err != nil {
			continue
		}
		var ve pluginreg.ValidationErrors
		if err := d.plugins.ValidateInput(m, c.Input); errors.As(err, &ve) {
			for _, fe := range ve {
				fields = append(fields, ProblemDetail{Message: fe.Message, Location: joinPath("steps["+c.Path+"].input", fe.Path)})
			}
		}
	}
	if len(fields) > 0 {
		return invalid("a step input does not match its plugin schema", fields...)
	}
	return nil
}

// initialSteps turns CreateStepInstance and ScheduleExecution actions into
// step instances with their first executions.
func initialSteps(actions []engine.Action) []store.NewStepInstance {
	var steps []store.NewStepInstance
	index := map[string]int{}
	pools := map[string]string{}
	for _, a := range actions {
		if c, ok := a.(engine.CreateStepInstance); ok {
			index[c.Path] = len(steps)
			pools[c.Path] = defaultPool
			if c.RunsOn != nil && c.RunsOn.Pool != "" {
				pools[c.Path] = c.RunsOn.Pool
			}
			steps = append(steps, store.NewStepInstance{Path: c.Path, Kind: c.Kind, Plugin: c.Plugin, Input: c.Input})
		}
	}
	for _, a := range actions {
		if s, ok := a.(engine.ScheduleExecution); ok {
			if i, found := index[s.Path]; found {
				steps[i].Execution = &store.NewExecution{Pool: pools[s.Path], ScheduledAt: s.ScheduledAt, Timeout: time.Duration(s.Timeout)}
			}
		}
	}
	return steps
}

// triggerFor is the trigger of a run started by an API request: it names the
// token's owner when the request sent one.
func triggerFor(ctx context.Context) Trigger {
	t := Trigger{Type: triggerAPI}
	if p, ok := PrincipalFrom(ctx); ok {
		t.Actor = p.Actor
	}
	return t
}
