package advance

import (
	"encoding/json"
	"fmt"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/store"
)

// DefaultPool is the pool of a step without runs_on.pool.
const DefaultPool = "default"

// toAdvance maps the engine's actions to one store change set. The actions of
// a Decide call are applied together or not at all.
//
// Mapping rules:
//   - a CreateStepInstance and the ScheduleExecution that follows it become
//     one pending AdvanceStep; a ScheduleExecution for an existing instance
//     becomes an AdvanceAttempt;
//   - SkipStep and FailStep become already finished steps;
//   - CancelExecution cancels a queued execution; an execution that was
//     already dispatched is cancelled by its owning controller, which sees
//     runs.cancel_requested_at (slice 53c), so there is nothing to write;
//   - the execution pool is runs_on.pool of the step in the job spec, else
//     DefaultPool; priority is 0; ScheduleExecution.Timeout is not stored,
//     the dispatcher re-reads it from the spec.
func toAdvance(acts []engine.Action, tree store.RunTree, job *engine.Job) (store.Advance, error) {
	var adv store.Advance
	created := map[string]int{} // path -> index in adv.NewSteps
	for _, act := range acts {
		var err error
		switch a := act.(type) {
		case engine.StartRun:
			adv.Start = true
		case engine.CreateStepInstance:
			created[a.Path] = len(adv.NewSteps)
			adv.NewSteps = append(adv.NewSteps, store.AdvanceStep{Path: a.Path, Plugin: a.Plugin, Input: a.Input})
		case engine.ScheduleExecution:
			err = schedule(&adv, created, a, job)
		case engine.SkipStep:
			adv.NewSteps = append(adv.NewSteps, store.AdvanceStep{Path: a.Path, Status: store.StepStatus(a.Status.Store()), Reason: a.Reason})
		case engine.FailStep:
			var reason []byte
			if reason, err = json.Marshal(a.Error); err == nil {
				adv.NewSteps = append(adv.NewSteps, store.AdvanceStep{Path: a.Path, Status: store.StepFailed, Reason: string(reason)})
			}
		case engine.CancelExecution:
			if queued(tree, a.ExecutionID) {
				adv.CancelQueued = append(adv.CancelQueued, a.ExecutionID)
			}
		case engine.CompleteRun:
			adv.Finish = store.RunStatus(a.Status.Store())
		default:
			err = fmt.Errorf("unsupported engine action %q", act.ActionType())
		}
		if err != nil {
			return store.Advance{}, fmt.Errorf("run %s: %w", tree.ID, err)
		}
	}
	return adv, nil
}

func schedule(adv *store.Advance, created map[string]int, a engine.ScheduleExecution, job *engine.Job) error {
	pool := poolFor(job, a.Path)
	ne := store.NewExecution{Pool: pool, ScheduledAt: a.ScheduledAt}
	if i, ok := created[a.Path]; ok {
		if adv.NewSteps[i].Execution != nil {
			return fmt.Errorf("step %q scheduled twice in one advance", a.Path)
		}
		adv.NewSteps[i].Execution = &ne
		return nil
	}
	adv.NewAttempts = append(adv.NewAttempts, store.AdvanceAttempt{Path: a.Path, NewExecution: ne})
	return nil
}

func poolFor(job *engine.Job, path string) string {
	for _, s := range job.Spec.Steps {
		if s.ID == path && s.RunsOn != nil && s.RunsOn.Pool != "" {
			return s.RunsOn.Pool
		}
	}
	return DefaultPool
}

func queued(tree store.RunTree, executionID string) bool {
	for _, n := range tree.Steps {
		for _, ex := range n.Executions {
			if ex.ID == executionID {
				return ex.State == store.ExecQueued
			}
		}
	}
	return false
}
