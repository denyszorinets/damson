package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

var _ store.ScheduleStore = (*Store)(nil)

func toSchedule(r gen.Schedule) store.Schedule {
	return store.Schedule{
		ID: r.ID, JobID: r.JobID, Name: r.Name, Cron: r.Cron, Timezone: r.Timezone, Options: json.RawMessage(r.Options),
		Enabled: r.Enabled, Misfire: r.Misfire, NextFireAt: tsPtr(r.NextFireAt), LastFireAt: tsPtr(r.LastFireAt),
		LastRunID: r.LastRunID, LastError: r.LastError, Version: r.Version, CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
}

func optionsText(o json.RawMessage) string {
	if len(o) == 0 {
		return "{}"
	}
	return string(o)
}

// CreateSchedule implements store.ScheduleStore.
func (s *Store) CreateSchedule(ctx context.Context, sc store.Schedule, now time.Time) (store.Schedule, error) {
	if err := sc.Validate(); err != nil {
		return store.Schedule{}, err
	}
	sc.Version, sc.CreatedAt, sc.UpdatedAt = 1, store.NormalizeTime(now), store.NormalizeTime(now)
	ev, err := store.NewScheduleEvent(ctx, store.ActionScheduleCreate, sc, now)
	if err != nil {
		return store.Schedule{}, err
	}
	err = s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		if _, err := q.GetJob(ctx, sc.JobID); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("job %q: %w", sc.JobID, store.ErrNotFound)
		} else if err != nil {
			return fmt.Errorf("reading job: %w", err)
		}
		err := q.InsertSchedule(ctx, gen.InsertScheduleParams{
			ID: sc.ID, JobID: sc.JobID, Name: sc.Name, Cron: sc.Cron, Timezone: sc.Timezone, Options: optionsText(sc.Options),
			Enabled: sc.Enabled, Misfire: sc.Misfire, NextFireAt: tsOpt(sc.NextFireAt), CreatedAt: ts(now), UpdatedAt: ts(now),
		})
		if isUniqueViolation(err) {
			return fmt.Errorf("schedule %q of job %q: %w", sc.Name, sc.JobID, store.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("creating schedule: %w", err)
		}
		if err := fillJobProject(ctx, tx.(*txn).q, &ev, sc.JobID); err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
	if err != nil {
		return store.Schedule{}, err
	}
	sc.Options = json.RawMessage(optionsText(sc.Options))
	return sc, nil
}

// GetSchedule implements store.ScheduleStore.
func (s *Store) GetSchedule(ctx context.Context, id string) (store.Schedule, error) {
	r, err := s.q.GetSchedule(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Schedule{}, fmt.Errorf("schedule %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Schedule{}, fmt.Errorf("reading schedule: %w", err)
	}
	return toSchedule(r), nil
}

// ListSchedules implements store.ScheduleStore.
func (s *Store) ListSchedules(ctx context.Context, jobID, afterID string, limit int) ([]store.Schedule, error) {
	rows, err := s.q.ListSchedules(ctx, gen.ListSchedulesParams{JobID: jobID, AfterID: afterID, RowLimit: int32(store.ClampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("listing schedules: %w", err)
	}
	out := make([]store.Schedule, len(rows))
	for i, r := range rows {
		out[i] = toSchedule(r)
	}
	return out, nil
}

// UpdateSchedule implements store.ScheduleStore.
func (s *Store) UpdateSchedule(ctx context.Context, sc store.Schedule, expected int64, now time.Time) (store.Schedule, error) {
	if err := sc.Validate(); err != nil {
		return store.Schedule{}, err
	}
	var out store.Schedule
	err := s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		n, err := q.UpdateScheduleCAS(ctx, gen.UpdateScheduleCASParams{
			Name: sc.Name, Cron: sc.Cron, Timezone: sc.Timezone, Options: optionsText(sc.Options), Enabled: sc.Enabled,
			Misfire: sc.Misfire, NextFireAt: tsOpt(sc.NextFireAt), UpdatedAt: ts(now), ID: sc.ID, ExpectedVersion: expected,
		})
		if isUniqueViolation(err) {
			return fmt.Errorf("schedule %q of job %q: %w", sc.Name, sc.JobID, store.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("updating schedule: %w", err)
		}
		cur, gerr := q.GetSchedule(ctx, sc.ID)
		if errors.Is(gerr, pgx.ErrNoRows) {
			return fmt.Errorf("schedule %q: %w", sc.ID, store.ErrNotFound)
		}
		if gerr != nil {
			return fmt.Errorf("reading schedule: %w", gerr)
		}
		if n == 0 {
			return fmt.Errorf("schedule %q is at version %d, not %d: %w", sc.ID, cur.Version, expected, store.ErrStale)
		}
		out = toSchedule(cur)
		ev, err := store.NewScheduleEvent(ctx, store.ActionScheduleUpdate, out, now)
		if err != nil {
			return err
		}
		if err := fillJobProject(ctx, tx.(*txn).q, &ev, out.JobID); err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
	return out, err
}

// DeleteSchedule implements store.ScheduleStore.
func (s *Store) DeleteSchedule(ctx context.Context, id string, now time.Time) error {
	if _, err := store.AuditFrom(ctx); err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		cur, err := q.GetSchedule(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("schedule %q: %w", id, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading schedule: %w", err)
		}
		if _, err := q.DeleteSchedule(ctx, id); err != nil {
			return fmt.Errorf("deleting schedule: %w", err)
		}
		ev, err := store.NewScheduleEvent(ctx, store.ActionScheduleDelete, toSchedule(cur), now)
		if err != nil {
			return err
		}
		if err := fillJobProject(ctx, tx.(*txn).q, &ev, cur.JobID); err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
}

// ListDueSchedules implements store.ScheduleStore.
func (s *Store) ListDueSchedules(ctx context.Context, now time.Time, limit int) ([]store.Schedule, error) {
	rows, err := s.q.ListDueSchedules(ctx, gen.ListDueSchedulesParams{Enabled: true, Now: ts(now), RowLimit: int32(store.ClampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("listing due schedules: %w", err)
	}
	out := make([]store.Schedule, len(rows))
	for i, r := range rows {
		out[i] = toSchedule(r)
	}
	return out, nil
}

// FireSchedule implements store.Tx.
func (t *txn) FireSchedule(ctx context.Context, f store.ScheduleFire) error {
	n, err := t.q.FireScheduleCAS(ctx, gen.FireScheduleCASParams{
		NextFireAt: ts(f.Next), LastFireAt: tsOpt(optTime(f.Fired)), LastRunID: f.RunID, LastError: f.Error,
		ID: f.ID, Enabled: true, ExpectedFireAt: ts(f.Expected), ExpectedVersion: f.ExpectedVersion,
	})
	if err != nil {
		return fmt.Errorf("firing schedule %q: %w", f.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("schedule %q was fired, changed or disabled: %w", f.ID, store.ErrStale)
	}
	return nil
}

func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// fillJobProject sets the project path of an audit event about a job's schedule
// or webhook, read in the transaction that records it, so a project admin finds
// the event under the project.
func fillJobProject(ctx context.Context, q *gen.Queries, ev *store.AuditEvent, jobID string) error {
	job, err := q.GetJob(ctx, jobID)
	if err != nil {
		return fmt.Errorf("reading the job for the audit event: %w", err)
	}
	p, err := q.GetProject(ctx, job.ProjectID)
	if err != nil {
		return fmt.Errorf("reading the project for the audit event: %w", err)
	}
	ev.ProjectPath = p.Path
	return nil
}

// ListSchedulesByProject implements store.ScheduleStore.
func (s *Store) ListSchedulesByProject(ctx context.Context, projectPath, afterID string, limit int) ([]store.Schedule, error) {
	rows, err := s.q.ListSchedulesByProject(ctx, gen.ListSchedulesByProjectParams{ProjectPath: projectPath, AfterID: afterID, RowLimit: int32(store.ClampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("listing schedules of project %s: %w", projectPath, err)
	}
	out := make([]store.Schedule, len(rows))
	for i, r := range rows {
		out[i] = toSchedule(r)
	}
	return out, nil
}
