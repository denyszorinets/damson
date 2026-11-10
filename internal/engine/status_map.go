package engine

// The store, the API and the agent protocol spell the cancelled state
// "canceled" (and call a not-yet-started run "pending"); the engine spells it
// "cancelled" and calls that run "queued". These helpers are the only place
// the two vocabularies meet: the orchestrator maps rows into RunState with
// the *FromStore functions and Decide's actions back with Store.
// They take and return plain strings so the engine imports nothing.

// RunStatusFromStore maps a stored run status ("pending", "running",
// "succeeded", "failed", "canceled") to the engine's.
func RunStatusFromStore(s string) RunStatus {
	switch s {
	case "pending":
		return RunQueued
	case "canceled":
		return RunCancelled
	}
	return RunStatus(s)
}

// Store returns the stored spelling of the run status.
func (s RunStatus) Store() string {
	switch s {
	case RunQueued:
		return "pending"
	case RunCancelled:
		return "canceled"
	}
	return string(s)
}

// StepStatusFromStore maps a stored step status to the engine's. The engine
// has no "running" step status: a running step is still pending.
func StepStatusFromStore(s string) StepStatus {
	switch s {
	case "running":
		return StepPending
	case "canceled":
		return StepCancelled
	}
	return StepStatus(s)
}

// Store returns the stored spelling of the step status.
func (s StepStatus) Store() string {
	if s == StepCancelled {
		return "canceled"
	}
	return string(s)
}

// ExecStateFromStore maps a stored execution state to the engine's. A
// timed-out attempt is a failure to the engine.
func ExecStateFromStore(s string) ExecState {
	switch s {
	case "canceled":
		return ExecCancelled
	case "timed_out":
		return ExecFailed
	}
	return ExecState(s)
}

// Store returns the stored spelling of the execution state.
func (s ExecState) Store() string {
	if s == ExecCancelled {
		return "canceled"
	}
	return string(s)
}
