package advance

import (
	"encoding/json"
	"fmt"
	"time"

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
//   - TimeoutRun becomes Advance.CancelRun (a sticky cancel with reason
//     timeout) and ScheduleWake actions are returned as wake times (the job
//     timeout deadline, the end of a finally grace), for the caller to queue in
//     the same transaction;
//   - a group is created by CreateStepInstance with Kind group (no execution;
//     the store starts it) and finished by CompleteGroup, which becomes
//     Advance.FinishGroups with the failure that decided it as JSON;
//   - SettleStep becomes Advance.Settle (the store finishes the step from the
//     outcome of that attempt) and a ScheduleExecution for an existing step
//     becomes an AdvanceAttempt carrying the attempt number, a retry when it
//     is above 1;
//   - CancelExecution cancels a queued execution; an execution that was
//     already dispatched is cancelled by its owning controller, which sees
//     runs.cancel_requested_at (slice 53c), so there is nothing to write;
//   - the execution pool is runs_on.pool of the step in the job spec, else
//     DefaultPool; priority is 0; ScheduleExecution.Timeout is stored on the
//     execution and sent to the agent, which enforces it.
func toAdvance(acts []engine.Action, tree store.RunTree, job *engine.Job) (adv store.Advance, wakes []time.Time, err error) {
	created := map[string]int{} // path -> index in adv.NewSteps
	for _, act := range acts {
		switch a := act.(type) {
		case engine.StartRun:
			adv.Start = true
		case engine.CreateStepInstance:
			created[a.Path] = len(adv.NewSteps)
			adv.NewSteps = append(adv.NewSteps, store.AdvanceStep{Path: a.Path, Kind: a.Kind, Plugin: a.Plugin, Input: a.Input})
		case engine.ScheduleExecution:
			err = schedule(&adv, created, a, job)
		case engine.SkipStep:
			adv.NewSteps = append(adv.NewSteps, store.AdvanceStep{Path: a.Path, Kind: a.Kind, Status: store.StepStatus(a.Status.Store()), Reason: a.Reason})
		case engine.FailStep:
			var reason []byte
			if reason, err = json.Marshal(a.Error); err == nil {
				adv.NewSteps = append(adv.NewSteps, store.AdvanceStep{Path: a.Path, Kind: a.Kind, Status: store.StepFailed, Reason: string(reason)})
			}
		case engine.CancelExecution:
			// The store cancels a queued execution and flags a claimed one for
			// its owning controller (Advance.Cancel).
			adv.Cancel = append(adv.Cancel, a.ExecutionID)
		case engine.CompleteGroup:
			var text []byte
			if a.Error != nil {
				if text, err = json.Marshal(a.Error); err != nil {
					break
				}
			}
			adv.FinishGroups = append(adv.FinishGroups, store.AdvanceFinishGroup{Path: a.Path, Status: store.StepStatus(a.Status.Store()), Error: string(text)})
		case engine.SettleStep:
			adv.Settle = append(adv.Settle, store.AdvanceSettle{Path: a.Path, Attempt: a.Attempt})
		case engine.TimeoutRun:
			adv.CancelRun = store.CancelReasonTimeout
		case engine.ScheduleWake:
			wakes = append(wakes, a.At)
		case engine.CompleteRun:
			adv.Finish = store.RunStatus(a.Status.Store())
		default:
			err = fmt.Errorf("unsupported engine action %q", act.ActionType())
		}
		if err != nil {
			return store.Advance{}, nil, fmt.Errorf("run %s: %w", tree.ID, err)
		}
	}
	return adv, wakes, nil
}

func schedule(adv *store.Advance, created map[string]int, a engine.ScheduleExecution, job *engine.Job) error {
	pool := poolFor(job, a.Path)
	ne := store.NewExecution{Pool: pool, ScheduledAt: a.ScheduledAt, Timeout: time.Duration(a.Timeout)}
	if i, ok := created[a.Path]; ok {
		if adv.NewSteps[i].Execution != nil {
			return fmt.Errorf("step %q scheduled twice in one advance", a.Path)
		}
		adv.NewSteps[i].Execution = &ne
		return nil
	}
	adv.NewAttempts = append(adv.NewAttempts, store.AdvanceAttempt{Path: a.Path, Attempt: a.Attempt, NewExecution: ne})
	return nil
}

func poolFor(job *engine.Job, path string) string {
	if s, ok := job.StepAt(path); ok && s.RunsOn != nil && s.RunsOn.Pool != "" {
		return s.RunsOn.Pool
	}
	return DefaultPool
}
