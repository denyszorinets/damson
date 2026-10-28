// Package advance runs run.advance: the River job that loads a run, asks the
// pure engine what to do next and applies the answer in one transaction
// (ARCHITECTURE section 8.3, ADR-0010).
//
// Enqueue joins the caller's store transaction, so "state changed but job
// lost" cannot happen. Dispatch is not a River job and lives elsewhere.
package advance

import (
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Kind is the River job kind of run.advance.
const Kind = "run.advance"

// QueueName is the River queue that carries run.advance jobs.
const QueueName = "advance"

// Args is the payload of a run.advance job.
//
// Version is runs.version at enqueue time and is part of the uniqueness key,
// see InsertOpts. The worker ignores it and always advances from the live row.
type Args struct {
	RunID   string `json:"run_id"`
	Version int64  `json:"version"`
}

// Kind implements river.JobArgs.
func (Args) Kind() string { return Kind }

// uniqueStates are the job states in which an identical job blocks a new
// insert. River requires available, pending, running and scheduled; we add
// retryable, and leave out completed, cancelled and discarded so a finished
// job never swallows a later enqueue.
func uniqueStates() []rivertype.JobState {
	return []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}
}

// InsertOpts implements river.JobArgsWithInsertOpts.
//
// Uniqueness is by (run_id, version) across uniqueStates. River cannot
// exclude "running" from the unique states, so a job keyed on run_id alone
// would swallow an enqueue that arrives while an advance is running, and that
// advance may already have read state from before the change: the wake-up
// would be lost. Every event that must wake a run bumps runs.version in its
// own transaction (CAS writes do it themselves, others call Tx.TouchRun), so
// the new job has a new key and is kept, while repeated enqueues at one
// version collapse into a single job.
func (Args) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:      QueueName,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: uniqueStates()},
	}
}
