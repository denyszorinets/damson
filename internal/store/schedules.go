package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Misfire policies of a schedule whose fire time passed while no scheduler ran.
const (
	MisfireRunOnce = "run_once" // fire one run for all the missed times
	MisfireSkip    = "skip"     // fire none; wait for the next time
)

// Audit actions of schedules.
const (
	ActionScheduleCreate = "schedule.create"
	ActionScheduleUpdate = "schedule.update"
	ActionScheduleDelete = "schedule.delete"
)

// AuditTargetSchedule is the audit target type of schedule events.
const AuditTargetSchedule = "schedule"

// Schedule starts runs of a job on a cron timetable (ARCHITECTURE 12.3).
type Schedule struct {
	ID         string
	JobID      string
	Name       string
	Cron       string
	Timezone   string
	Options    json.RawMessage // JSON object
	Enabled    bool
	Misfire    string
	NextFireAt *time.Time // nil while disabled
	LastFireAt *time.Time
	LastRunID  string
	LastError  string // why the last fire made no run; never an option value
	Version    int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Validate checks the fields the store itself depends on. Cron syntax and the
// time zone are checked by the caller, which also computes NextFireAt.
func (s Schedule) Validate() error {
	switch {
	case s.ID == "" || s.JobID == "":
		return fmt.Errorf("a schedule needs an id and a job: %w", ErrInvalid)
	case s.Name == "" || s.Cron == "" || s.Timezone == "":
		return fmt.Errorf("a schedule needs a name, a cron expression and a time zone: %w", ErrInvalid)
	case s.Misfire != MisfireRunOnce && s.Misfire != MisfireSkip:
		return fmt.Errorf("schedule misfire %q must be %s or %s: %w", s.Misfire, MisfireRunOnce, MisfireSkip, ErrInvalid)
	case len(s.Options) > 0 && !json.Valid(s.Options):
		return fmt.Errorf("schedule options are not JSON: %w", ErrInvalid)
	case s.Enabled && s.NextFireAt == nil:
		return fmt.Errorf("an enabled schedule needs a next fire time: %w", ErrInvalid)
	}
	return nil
}

// ScheduleFire moves a due schedule on after the scheduler handled it.
type ScheduleFire struct {
	ID string
	// Expected is the next_fire_at the scheduler read; the move is a CAS on it.
	Expected time.Time
	// ExpectedVersion is the schedule version the scheduler read; an edit made
	// since then makes the fire stale.
	ExpectedVersion int64
	// Next is the new next_fire_at.
	Next time.Time
	// Fired is when the run was started; zero if the fire time was skipped or failed.
	Fired time.Time
	// RunID is the created run, empty if none.
	RunID string
	// Error says why no run was made: a short text without option values.
	Error string
}

// ScheduleAuditDetails is the only content of a schedule event's details.
// Options are left out: they may name secret refs.
type ScheduleAuditDetails struct {
	JobID    string `json:"job_id"`
	Name     string `json:"name"`
	Cron     string `json:"cron"`
	Timezone string `json:"timezone"`
	Enabled  bool   `json:"enabled"`
	Misfire  string `json:"misfire"`
	Version  int64  `json:"version"`
}

// NewScheduleEvent builds the event of a schedule write for the actor in ctx.
func NewScheduleEvent(ctx context.Context, action string, s Schedule, now time.Time) (AuditEvent, error) {
	meta, err := AuditFrom(ctx)
	if err != nil {
		return AuditEvent{}, err
	}
	id, err := NewID()
	if err != nil {
		return AuditEvent{}, err
	}
	details, err := json.Marshal(ScheduleAuditDetails{
		JobID: s.JobID, Name: s.Name, Cron: s.Cron, Timezone: s.Timezone, Enabled: s.Enabled, Misfire: s.Misfire, Version: s.Version,
	})
	if err != nil {
		return AuditEvent{}, fmt.Errorf("encoding audit details: %w", err)
	}
	return AuditEvent{
		ID: id, At: NormalizeTime(now), Actor: meta.Actor, Action: action, TargetType: AuditTargetSchedule,
		Target: s.ID, Details: details, RequestID: meta.RequestID,
	}, nil
}

// ScheduleStore persists schedules. Writes are audited: they fail with
// ErrInvalid without an actor in ctx (WithAudit) and record the event in the
// same transaction.
type ScheduleStore interface {
	// CreateSchedule returns ErrNotFound if the job is absent and ErrConflict
	// if the job already has a schedule of that name.
	CreateSchedule(ctx context.Context, s Schedule, now time.Time) (Schedule, error)
	// GetSchedule returns ErrNotFound if absent.
	GetSchedule(ctx context.Context, id string) (Schedule, error)
	// ListSchedules lists the schedules of a job (all when jobID is empty) in
	// id order, after afterID, at most limit.
	ListSchedules(ctx context.Context, jobID, afterID string, limit int) ([]Schedule, error)
	// UpdateSchedule replaces name, cron, timezone, options, enabled, misfire
	// and next fire time, as a CAS on the version. ErrNotFound if absent,
	// ErrStale if the version moved, ErrConflict if the name is taken.
	UpdateSchedule(ctx context.Context, s Schedule, expectedVersion int64, now time.Time) (Schedule, error)
	// DeleteSchedule returns ErrNotFound if absent.
	DeleteSchedule(ctx context.Context, id string, now time.Time) error
	// ListDueSchedules returns the enabled schedules whose next fire time is at
	// or before now, oldest first.
	ListDueSchedules(ctx context.Context, now time.Time, limit int) ([]Schedule, error)
}
