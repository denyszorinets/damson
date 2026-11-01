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
//
// Sweep is the sweep epoch (unix ms) of a job inserted by Sweep and 0 for an
// event enqueue; omitempty keeps the latter's JSON, and so its unique key,
// unchanged.
type Args struct {
	RunID   string `json:"run_id"`
	Version int64  `json:"version"`
	Sweep   int64  `json:"sweep,omitempty"`
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
// Uniqueness is by (run_id, version, sweep epoch) across uniqueStates. River cannot
// exclude "running" from the unique states, so a job keyed on run_id alone
// would swallow an enqueue that arrives while an advance is running, and that
// advance may already have read state from before the change: the wake-up
// would be lost. Every event that must wake a run bumps runs.version in its
// own transaction (CAS writes do it themselves, others call Tx.TouchRun), so
// the new job has a new key and is kept, while repeated enqueues at one
// version collapse into a single job.
//
// The sweep epoch is for crash recovery: a controller that dies mid-advance
// leaves its job "running" (River's rescuer only gets to it later) and the run
// version does not change, so the sweep's job at the same version would
// collapse into the dead one. A sweep job carries its epoch and so has a key
// of its own: the stranded job is bypassed within one sweep interval.
//
// MaxAttempts is 5: with River's default of 25 and its attempt^4 backoff a
// failing job would sit in "retryable" for weeks; after five the job is
// discarded and the next sweep enqueues the run again under a new epoch.
func (Args) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueName,
		MaxAttempts: 5,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByState: uniqueStates()},
	}
}
