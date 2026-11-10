package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// RunPrune counts what one PruneRuns batch deleted.
type RunPrune struct {
	Runs       int64 `json:"runs"`
	Executions int64 `json:"executions"`
	Chunks     int64 `json:"log_chunks"`
	Objects    int64 `json:"log_objects"`
	// Queued is how many deferred deletes of external log objects were queued.
	Queued int64 `json:"queued_log_deletes"`
}

// RunPruneDetails is the details of a run.prune event: counts and the cutoff,
// never any content of the deleted runs.
type RunPruneDetails struct {
	RunPrune
	Before time.Time `json:"before"`
}

// NewRunPruneEvent builds the event of one run prune batch for the actor in ctx.
func NewRunPruneEvent(ctx context.Context, d RunPruneDetails, now time.Time) (AuditEvent, error) {
	meta, err := AuditFrom(ctx)
	if err != nil {
		return AuditEvent{}, err
	}
	id, err := NewID()
	if err != nil {
		return AuditEvent{}, err
	}
	d.Before = NormalizeTime(d.Before)
	details, err := json.Marshal(d)
	if err != nil {
		return AuditEvent{}, fmt.Errorf("encoding audit details: %w", err)
	}
	return AuditEvent{
		ID: id, At: NormalizeTime(now), Actor: meta.Actor, Action: ActionRunPrune, TargetType: AuditTargetInstance,
		Target: "runs", Details: details, RequestID: meta.RequestID,
	}, nil
}
