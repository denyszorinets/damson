package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

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
	job, err := d.ds.GetJob(ctx, in.ID)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	number := job.CurrentVersion
	if body.Version != nil {
		number = *body.Version
	}
	ver, err := d.ds.GetJobVersionByNumber(ctx, job.ID, number)
	if errors.Is(err, store.ErrNotFound) && body.Version != nil {
		return nil, apiError(ctx, invalid("unknown job version",
			ProblemDetail{Message: "no such version", Location: "body.version"}))
	}
	if err != nil {
		return nil, apiError(ctx, err)
	}
	spec, err := d.eng.Parse(ver.Spec)
	if err != nil {
		return nil, apiError(ctx, fmt.Errorf("stored spec of job %s version %d: %w", job.ID, number, err))
	}
	opts, err := resolveOptions(spec, body.Options)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	nr, err := d.newRun(job, ver, spec, opts)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	tree, err := d.ds.CreateRun(ctx, nr, d.now())
	if err != nil {
		return nil, apiError(ctx, err)
	}
	return &runDetailOutput{Body: toRunDetail(tree, ver.Version)}, nil
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
	err = pluginreg.ValidateJSONSchema("options", schema, input)
	var ve pluginreg.ValidationErrors
	switch {
	case err == nil:
		return opts, nil
	case errors.Is(err, pluginreg.ErrInputTooLarge):
		return nil, invalid("options are too large")
	case errors.As(err, &ve):
		fields := make([]ProblemDetail, len(ve))
		for i, fe := range ve {
			fields[i] = ProblemDetail{Message: fe.Message, Location: "options." + fe.Path}
			if fe.Path == "$" {
				fields[i].Location = "options"
			}
		}
		return nil, invalid("the options are invalid", fields...)
	}
	return nil, fmt.Errorf("validating options: %w", err)
}

// newRun builds the run insert from what Decide returns for a queued run: the
// step instances it creates and their first executions. The other actions
// (start, skip, fail, complete) are applied by run.advance.
func (d deps) newRun(job store.Job, ver store.JobVersion, spec *engine.Job, opts map[string]any) (store.NewRun, error) {
	id, err := store.NewID()
	if err != nil {
		return store.NewRun{}, err
	}
	trigger, err := json.Marshal(map[string]string{"type": triggerAPI})
	if err != nil {
		return store.NewRun{}, fmt.Errorf("encoding trigger: %w", err)
	}
	options, err := json.Marshal(opts)
	if err != nil {
		return store.NewRun{}, fmt.Errorf("encoding options: %w", err)
	}
	now := d.now()
	state := engine.RunState{
		ID: id, Status: engine.RunQueued, Trigger: triggerAPI, Inputs: opts,
		Job: engine.JobInfo{Name: spec.Metadata.Name, Project: spec.Metadata.Project, Version: strconv.Itoa(ver.Version)},
	}
	actions, err := d.eng.Decide(spec, state, now)
	if err != nil {
		return store.NewRun{}, fmt.Errorf("deciding the initial state of run %s: %w", id, err)
	}
	return store.NewRun{
		ID: id, JobID: job.ID, JobVersionID: ver.ID, Trigger: trigger, Options: options,
		Steps: initialSteps(actions),
	}, nil
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
			steps = append(steps, store.NewStepInstance{Path: c.Path, Plugin: c.Plugin, Input: c.Input})
		}
	}
	for _, a := range actions {
		if s, ok := a.(engine.ScheduleExecution); ok {
			if i, found := index[s.Path]; found {
				steps[i].Execution = &store.NewExecution{Pool: pools[s.Path], ScheduledAt: s.ScheduledAt}
			}
		}
	}
	return steps
}
