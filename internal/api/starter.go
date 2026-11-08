package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/store"
)

// Trigger says what started a run. It is stored as run.trigger (JSON) and its
// Type is the engine's run.trigger value.
type Trigger struct {
	Type       string `json:"type"`                  // api, schedule or webhook
	ScheduleID string `json:"schedule_id,omitempty"` // schedule
	WebhookID  string `json:"webhook_id,omitempty"`  // webhook
	FireAt     string `json:"fire_at,omitempty"`     // schedule: the RFC 3339 fire time
	Actor      string `json:"actor,omitempty"`       // api: who sent the token (sa:<id> or admin-token:<hash prefix>)
}

// Trigger types.
const (
	TriggerSchedule = "schedule"
	TriggerWebhook  = "webhook"
)

// prepareRun is the part of starting a run that needs no transaction: it reads
// the job version (the current one unless version is set), validates the
// options against spec.options and builds the run with its first steps.
func (d deps) prepareRun(ctx context.Context, jobID string, version *int, given map[string]any, trig Trigger) (store.NewRun, int, error) {
	job, ver, spec, err := d.loadSpec(ctx, jobID, version)
	if err != nil {
		return store.NewRun{}, 0, err
	}
	opts, err := resolveOptions(spec, given)
	if err != nil {
		return store.NewRun{}, 0, err
	}
	nr, err := d.newRun(ctx, job, ver, spec, opts, trig)
	return nr, ver.Version, err
}

// loadSpec reads a job's version (the current one unless version is set) and
// parses its spec.
func (d deps) loadSpec(ctx context.Context, jobID string, version *int) (store.Job, store.JobVersion, *engine.Job, error) {
	job, err := d.ds.GetJob(ctx, jobID)
	if err != nil {
		return store.Job{}, store.JobVersion{}, nil, err
	}
	if job.ArchivedAt != nil {
		return store.Job{}, store.JobVersion{}, nil, &store.ManagedError{What: "job " + job.Name, By: "archived"}
	}
	number := job.CurrentVersion
	if version != nil {
		number = *version
	}
	ver, err := d.ds.GetJobVersionByNumber(ctx, job.ID, number)
	if errors.Is(err, store.ErrNotFound) && version != nil {
		return store.Job{}, store.JobVersion{}, nil, invalid("unknown job version", ProblemDetail{Message: "no such version", Location: "body.version"})
	}
	if err != nil {
		return store.Job{}, store.JobVersion{}, nil, err
	}
	spec, err := d.eng.Parse(ver.Spec)
	if err != nil {
		return store.Job{}, store.JobVersion{}, nil, fmt.Errorf("stored spec of job %s version %d: %w", job.ID, number, err)
	}
	return job, ver, spec, nil
}

// Starter starts runs for triggers other than the API (schedules, webhooks).
// It is the same path the API trigger takes: the options are validated against
// the job's spec.options and the run is built with the same initial steps and
// frozen targets.
type Starter struct{ d deps }

// NewStarter builds a Starter from the same Config the server uses (Data,
// Targets, Plugins, Advance and Now matter).
func NewStarter(cfg Config) *Starter { return &Starter{d: newDeps(cfg)} }

// Prepared is a run ready to insert.
type Prepared struct {
	nr      store.NewRun
	project string // path of the job's project, for the audit event
	trig    Trigger
}

// StartError is a refusal that is the job's or the trigger's fault, such as
// options that do not match the spec. Its text names fields, never values.
type StartError struct{ Msg string }

func (e *StartError) Error() string { return e.Msg }

// Prepare validates the options and builds the run for the job's current
// version. It must be called outside any transaction. A *StartError means the
// run can never start with these inputs; other errors are transient.
func (s *Starter) Prepare(ctx context.Context, jobID string, options map[string]any, trig Trigger) (Prepared, error) {
	if err := s.d.ready(); err != nil {
		return Prepared{}, err
	}
	nr, _, err := s.d.prepareRun(ctx, jobID, nil, options, trig)
	if err != nil {
		return Prepared{}, startError(err)
	}
	project, err := s.d.projectOfJobRow(ctx, jobID)
	if err != nil {
		return Prepared{}, startError(err)
	}
	return Prepared{nr: nr, project: project, trig: trig}, nil
}

// CheckOptions reports whether options are acceptable for the job now, with
// the schema defaults applied. It returns a *StartError when they are not.
func (s *Starter) CheckOptions(ctx context.Context, jobID string, options map[string]any) error {
	if err := s.d.ready(); err != nil {
		return err
	}
	_, _, spec, err := s.d.loadSpec(ctx, jobID, nil)
	if err == nil {
		_, err = resolveOptions(spec, options)
	}
	return startError(err)
}

// startError turns a refusal that is the caller's fault into a *StartError.
func startError(err error) error {
	var inv *invalidError
	var managed *store.ManagedError
	switch {
	case errors.As(err, &inv):
		return &StartError{Msg: inv.message()}
	case errors.As(err, &managed):
		return &StartError{Msg: "the job is archived"} // an archived job cannot run; a schedule must not stay due forever
	case errors.Is(err, store.ErrNotFound):
		return &StartError{Msg: "the job no longer exists"}
	}
	return err
}

// Create inserts the prepared run in tx, with run.advance enqueued in the same
// transaction when an orchestrator is wired, and returns the run id.
func (s *Starter) Create(ctx context.Context, tx store.Tx, p Prepared) (string, error) {
	tree, err := tx.CreateRun(ctx, p.nr, s.d.now())
	if err != nil {
		return "", err
	}
	if err := s.d.auditRunTrigger(ctx, tx, p.project, p.nr, tree.ID, p.trig); err != nil {
		return "", err
	}
	if s.d.advance != nil {
		if err := s.d.advance.Enqueue(ctx, tx, tree.ID); err != nil {
			return "", err
		}
	}
	return tree.ID, nil
}

// message is the error text with the locations of its fields.
func (e *invalidError) message() string {
	if len(e.fields) == 0 {
		return e.detail
	}
	parts := make([]string, 0, len(e.fields))
	for _, f := range e.fields {
		parts = append(parts, f.Location+": "+f.Message)
	}
	return e.detail + " (" + strings.Join(parts, "; ") + ")"
}

// runTriggerDetails is the content of a run.trigger event: ids and the trigger
// kind, never the options.
type runTriggerDetails struct {
	JobID        string `json:"job_id"`
	JobVersionID string `json:"job_version_id"`
	Trigger      string `json:"trigger"`
	ScheduleID   string `json:"schedule_id,omitempty"`
	WebhookID    string `json:"webhook_id,omitempty"`
}

// triggerActor is who a run is attributed to when the context names nobody:
// the duty or webhook behind it.
func triggerActor(t Trigger) string {
	switch {
	case t.Type == TriggerSchedule:
		return "system:scheduler"
	case t.Type == TriggerWebhook:
		return "webhook:" + t.WebhookID
	case t.Actor != "":
		return t.Actor
	}
	return "anonymous"
}

// auditRunTrigger appends the run.trigger event in the transaction that
// creates the run.
func (d deps) auditRunTrigger(ctx context.Context, tx store.Tx, project string, nr store.NewRun, runID string, trig Trigger) error {
	meta, err := store.AuditFrom(ctx)
	if err != nil {
		meta = store.AuditMeta{Actor: triggerActor(trig)}
	}
	ev, err := store.NewEventAs(meta, store.ActionRunTrigger, store.AuditTargetRun, runID, project, runTriggerDetails{
		JobID: nr.JobID, JobVersionID: nr.JobVersionID, Trigger: trig.Type, ScheduleID: trig.ScheduleID, WebhookID: trig.WebhookID,
	}, d.now())
	if err != nil {
		return err
	}
	return tx.AppendAuditEvent(ctx, ev)
}
