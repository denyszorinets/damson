package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/schedule"
	"github.com/denyszorinets/damson/internal/store"
)

// Schedule starts runs of a job on a cron timetable.
type Schedule struct {
	ID         string         `json:"id"`
	JobID      string         `json:"job_id"`
	Name       string         `json:"name"`
	Cron       string         `json:"cron" doc:"Five fields (minute hour day-of-month month day-of-week) or @hourly, @daily, @weekly, @monthly, @yearly."`
	Timezone   string         `json:"timezone" doc:"An IANA zone name."`
	Options    map[string]any `json:"options" doc:"The options each run starts with; checked against spec.options when saved and again when the schedule fires."`
	Enabled    bool           `json:"enabled"`
	Misfire    string         `json:"misfire" doc:"run_once starts one run for the missed fire times after downtime; skip starts none."`
	NextFireAt *time.Time     `json:"next_fire_at,omitempty" doc:"Absent while disabled."`
	LastFireAt *time.Time     `json:"last_fire_at,omitempty" doc:"When the last run was started."`
	LastRunID  string         `json:"last_run_id,omitempty"`
	LastError  string         `json:"last_error,omitempty" doc:"Why the last fire time made no run, or a warning about the schedule. Never an option value."`
	Version    int64          `json:"version"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

func toSchedule(s store.Schedule) Schedule {
	opts := map[string]any{}
	_ = json.Unmarshal(s.Options, &opts) // stored from a map; a failure leaves it empty
	return Schedule{
		ID: s.ID, JobID: s.JobID, Name: s.Name, Cron: s.Cron, Timezone: s.Timezone, Options: opts, Enabled: s.Enabled, Misfire: s.Misfire,
		NextFireAt: s.NextFireAt, LastFireAt: s.LastFireAt, LastRunID: s.LastRunID, LastError: s.LastError,
		Version: s.Version, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

type scheduleBody struct{ Body Schedule }

type scheduleIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

type listSchedulesInput struct {
	JobID   string `query:"job_id" maxLength:"64" doc:"Only the schedules of this job."`
	Project string `query:"project" maxLength:"256" doc:"Only the schedules of the jobs of this project and the projects below it. A user needs job_id or project; a project the caller holds no role on answers 404."`
	Cursor  string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor."`
	Limit   int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
}

type listSchedulesOutput struct {
	Body struct {
		Items      []Schedule `json:"items"`
		NextCursor string     `json:"next_cursor,omitempty" doc:"Empty on the last page."`
	}
}

type createScheduleInput struct {
	Body struct {
		JobID    string         `json:"job_id" required:"true" maxLength:"64"`
		Name     string         `json:"name" required:"true" maxLength:"63"`
		Cron     string         `json:"cron" required:"true" maxLength:"128"`
		Timezone string         `json:"timezone,omitempty" maxLength:"64" doc:"Default UTC."`
		Options  map[string]any `json:"options,omitempty"`
		Enabled  *bool          `json:"enabled,omitempty" doc:"Default true."`
		Misfire  string         `json:"misfire,omitempty" enum:"run_once,skip," doc:"Default run_once."`
	}
}

// patchScheduleInput: every field is optional. options replaces the whole value.
type patchScheduleInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		IfVersion *int64          `json:"if_version,omitempty" minimum:"1" doc:"The version the caller read; 409 if it moved. Omitted: the version current when handled."`
		Name      *string         `json:"name,omitempty" maxLength:"63"`
		Cron      *string         `json:"cron,omitempty" maxLength:"128"`
		Timezone  *string         `json:"timezone,omitempty" maxLength:"64"`
		Options   *map[string]any `json:"options,omitempty"`
		Enabled   *bool           `json:"enabled,omitempty"`
		Misfire   *string         `json:"misfire,omitempty" enum:"run_once,skip"`
	}
}

func registerSchedules(api huma.API, d deps) {
	op := func(id, method, path, summary string, status int, errs ...int) huma.Operation {
		return huma.Operation{
			OperationID: id, Method: method, Path: path, Summary: summary, Tags: []string{"schedules"},
			Security: adminSecurity, DefaultStatus: status,
			Errors: append([]int{http.StatusUnauthorized, http.StatusServiceUnavailable}, errs...),
		}
	}
	huma.Register(api, op("list-schedules", http.MethodGet, "/schedules", "List schedules", 0, http.StatusBadRequest), func(ctx context.Context, in *listSchedulesInput) (*listSchedulesOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		after, err := decodeCursor(ctx, in.Cursor)
		if err != nil {
			return nil, err
		}
		if err := d.checkTriggerList(ctx, in.JobID, in.Project); err != nil {
			return nil, err
		}
		var rows []store.Schedule
		if in.Project != "" {
			rows, err = d.ds.ListSchedulesByProject(ctx, in.Project, after, in.Limit+1)
		} else {
			rows, err = d.ds.ListSchedules(ctx, in.JobID, after, in.Limit+1)
		}
		if err != nil {
			return nil, apiError(ctx, err)
		}
		out := &listSchedulesOutput{}
		out.Body.Items = []Schedule{}
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			out.Body.NextCursor = encodeCursor(rows[len(rows)-1].ID)
		}
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, toSchedule(r))
		}
		return out, nil
	})
	huma.Register(api, op("get-schedule", http.MethodGet, "/schedules/{id}", "Get a schedule", 0, http.StatusNotFound), func(ctx context.Context, in *scheduleIDInput) (*scheduleBody, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		s, err := d.ds.GetSchedule(ctx, in.ID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		return &scheduleBody{Body: toSchedule(s)}, nil
	})
	huma.Register(api, op("create-schedule", http.MethodPost, "/schedules", "Create a schedule", http.StatusCreated,
		http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity), func(ctx context.Context, in *createScheduleInput) (*scheduleBody, error) {
		return createSchedule(ctx, d, in)
	})
	huma.Register(api, op("patch-schedule", http.MethodPatch, "/schedules/{id}", "Change a schedule", 0,
		http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity), func(ctx context.Context, in *patchScheduleInput) (*scheduleBody, error) {
		return patchSchedule(ctx, d, in)
	})
	huma.Register(api, op("delete-schedule", http.MethodDelete, "/schedules/{id}", "Delete a schedule", http.StatusNoContent, http.StatusNotFound), func(ctx context.Context, in *scheduleIDInput) (*struct{}, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		if err := d.ds.DeleteSchedule(ctx, in.ID, d.now()); err != nil {
			return nil, apiError(ctx, err)
		}
		return nil, nil
	})
}

// checkSchedule validates what a schedule needs to be saved and computes its
// next fire time: the cron expression and zone, and the options against the
// job's spec.options now.
func (d deps) checkSchedule(ctx context.Context, s *store.Schedule, recompute, checkOptions bool) error {
	spec, err := schedule.Parse(s.Cron, s.Timezone)
	if err != nil {
		return invalid("the schedule is invalid", ProblemDetail{Message: err.Error(), Location: "body.cron"})
	}
	var opts map[string]any
	if len(s.Options) > 0 {
		if err := json.Unmarshal(s.Options, &opts); err != nil {
			return invalid("the schedule is invalid", ProblemDetail{Message: "options must be an object", Location: "body.options"})
		}
	}
	// Options are checked when they are set or the schedule is switched on, so a
	// job whose options changed underneath can still have its schedules disabled.
	if checkOptions {
		_, _, job, err := d.loadSpec(ctx, s.JobID, nil)
		if err != nil {
			return err
		}
		if _, err := resolveOptions(job, opts); err != nil {
			return err
		}
	}
	switch {
	case !s.Enabled:
		s.NextFireAt = nil
	case recompute || s.NextFireAt == nil:
		next, ok := spec.Next(d.now())
		if !ok {
			return invalid("the schedule is invalid", ProblemDetail{Message: "the expression never fires", Location: "body.cron"})
		}
		s.NextFireAt = &next
	}
	return nil
}

func createSchedule(ctx context.Context, d deps, in *createScheduleInput) (*scheduleBody, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	b := in.Body
	if err := d.requireOnJob(ctx, b.JobID, store.RoleEditor); err != nil {
		return nil, apiError(ctx, err)
	}
	id, err := store.NewID()
	if err != nil {
		return nil, apiError(ctx, err)
	}
	opts, err := json.Marshal(nonNilMap(b.Options))
	if err != nil {
		return nil, apiError(ctx, fmt.Errorf("encoding options: %w", err))
	}
	s := store.Schedule{
		ID: id, JobID: b.JobID, Name: b.Name, Cron: b.Cron, Timezone: orDefault(b.Timezone, "UTC"), Options: opts,
		Enabled: b.Enabled == nil || *b.Enabled, Misfire: orDefault(b.Misfire, store.MisfireRunOnce),
	}
	if err := d.checkSchedule(ctx, &s, true, true); err != nil {
		return nil, apiError(ctx, err)
	}
	created, err := d.ds.CreateSchedule(ctx, s, d.now())
	if err != nil {
		return nil, apiError(ctx, err)
	}
	return &scheduleBody{Body: toSchedule(created)}, nil
}

func patchSchedule(ctx context.Context, d deps, in *patchScheduleInput) (*scheduleBody, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	for range maxPatchAttempts {
		cur, err := d.ds.GetSchedule(ctx, in.ID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		expected := cur.Version
		if in.Body.IfVersion != nil {
			if *in.Body.IfVersion != cur.Version {
				return nil, apiError(ctx, store.ErrStale)
			}
			expected = *in.Body.IfVersion
		}
		s, recompute, err := applySchedulePatch(cur, in)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		if err := d.checkSchedule(ctx, &s, recompute, in.Body.Options != nil || in.Body.Enabled != nil && *in.Body.Enabled && !cur.Enabled); err != nil {
			return nil, apiError(ctx, err)
		}
		updated, err := d.ds.UpdateSchedule(ctx, s, expected, d.now())
		if err == nil {
			return &scheduleBody{Body: toSchedule(updated)}, nil
		}
		// Without if_version a lost race is retried on the fresh row.
		if in.Body.IfVersion != nil || !errors.Is(err, store.ErrStale) {
			return nil, apiError(ctx, err)
		}
	}
	return nil, apiError(ctx, store.ErrStale)
}

// applySchedulePatch returns cur with the patch applied, and whether the next
// fire time must be computed again (the timetable changed or it was disabled).
func applySchedulePatch(cur store.Schedule, in *patchScheduleInput) (store.Schedule, bool, error) {
	b := in.Body
	s := cur
	recompute := false
	if b.Name != nil {
		s.Name = *b.Name
	}
	if b.Cron != nil && *b.Cron != s.Cron {
		s.Cron, recompute = *b.Cron, true
	}
	if b.Timezone != nil && *b.Timezone != s.Timezone {
		s.Timezone, recompute = *b.Timezone, true
	}
	if b.Misfire != nil {
		s.Misfire = *b.Misfire
	}
	if b.Enabled != nil {
		recompute = recompute || (*b.Enabled && !s.Enabled)
		s.Enabled = *b.Enabled
	}
	if b.Options != nil {
		raw, err := json.Marshal(nonNilMap(*b.Options))
		if err != nil {
			return s, false, fmt.Errorf("encoding options: %w", err)
		}
		s.Options = raw
	}
	return s, recompute, nil
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
