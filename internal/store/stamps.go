package store

import "context"

// RunStamp is a cheap fingerprint of a run tree: it differs whenever the run
// version, a step instance's status or an execution's state moved. Streams
// compare it between polls and load the full tree only when it changed.
type RunStamp struct {
	Version         int64
	Steps           int64
	StepsDone       int64
	Execs           int64
	ExecsDone       int64
	ExecsRunning    int64
	ExecsDispatched int64
}

// LogStamp is a cheap fingerprint of an execution's log: its state and the
// stored chunk manifest (count and highest sequence number).
type LogStamp struct {
	State  ExecutionState
	Chunks int64
	MaxSeq int64
}

// StampStore serves the change markers.
type StampStore interface {
	// GetRunStamp returns ErrNotFound if the run is absent.
	GetRunStamp(ctx context.Context, runID string) (RunStamp, error)
	// GetLogStamp returns ErrNotFound if the execution is absent.
	GetLogStamp(ctx context.Context, executionID string) (LogStamp, error)
}
