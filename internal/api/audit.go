package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/store"
)

// AuditEvent is one entry of the audit trail. Details holds ids, versions and
// field names; it never holds a secret value.
type AuditEvent struct {
	ID          string          `json:"id"`
	At          time.Time       `json:"at"`
	Actor       string          `json:"actor" doc:"user:<sub>, sa:<service account id>, admin-token:<hash prefix>, cli:<os user>, system:<duty>, webhook:<id> or anonymous."`
	ActorName   string          `json:"actor_name,omitempty" doc:"A display snapshot of the actor, such as a user's email at that time."`
	Action      string          `json:"action" doc:"secret.create, secret.update, secret.delete, secret.kek_rotate, log.purge, admin_token.create, admin_token.revoke, metrics_token.create, metrics_token.revoke, audit.prune, run.prune, schedule.create, schedule.update, schedule.delete, webhook.create, webhook.revoke, project.create, project.update, project.delete, project.access_rules.update, job.access_rules.update, service_account.create, service_account.delete, service_token.create, service_token.revoke, job.create, job.version.create, run.trigger, run.cancel, access.check, auth.login or secret.read."`
	TargetType  string          `json:"target_type" doc:"secret, execution, schedule, webhook, project, job, run, user, service_account, service_token or instance."`
	Target      string          `json:"target" doc:"<scope>/<name> for a secret, the execution id for log.purge."`
	ProjectPath string          `json:"project_path,omitempty"`
	Details     json.RawMessage `json:"details" doc:"A JSON object; never a secret value."`
	RequestID   string          `json:"request_id,omitempty"`
}

type listAuditInput struct {
	Project    string `query:"project" maxLength:"600" doc:"Only events of this project and its descendants. Required for a caller who is not an instance admin, who must be admin of the project."`
	TargetType string `query:"target_type" maxLength:"32" doc:"Only this target type."`
	Target     string `query:"target" maxLength:"800" doc:"Only this target."`
	Cursor     string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor."`
	Limit      int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
}

type listAuditOutput struct {
	Body struct {
		Items      []AuditEvent `json:"items" doc:"Newest first."`
		NextCursor string       `json:"next_cursor,omitempty" doc:"Empty on the last page."`
	}
}

type purgeLogsOutput struct {
	Body struct {
		Chunks  int64 `json:"chunks" doc:"Log chunks removed."`
		Objects int64 `json:"objects" doc:"Log objects removed from the database."`
		Queued  int64 `json:"queued" doc:"Object prefixes queued for deletion from an external log store (s3); removed within about a minute."`
	}
}

func registerAudit(api huma.API, d deps) {
	huma.Register(api, huma.Operation{
		OperationID: "purge-execution-logs", Method: http.MethodDelete, Path: "/executions/{id}/logs",
		Summary: "Purge the logs of a finished execution", Tags: []string{"executions"}, Security: adminSecurity,
		Middlewares: huma.Middlewares{requireAdmin(api, d)},
		Description: "For a leak the masker missed (ADR-0047). Removes every stored log chunk of the execution and writes a log.purge audit event. 409 while the execution can still write logs; cancel it first. Repeating it is allowed.",
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *executionInput) (*purgeLogsOutput, error) {
		res, err := d.ds.PurgeExecutionLogs(ctx, in.ID, d.now())
		if err != nil {
			return nil, apiError(ctx, err)
		}
		out := &purgeLogsOutput{}
		out.Body.Chunks, out.Body.Objects, out.Body.Queued = res.Chunks, res.Objects, res.Queued
		return out, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "list-audit-events", Method: http.MethodGet, Path: "/audit",
		Summary: "List audit events", Tags: []string{"audit"}, Security: adminSecurity,
		Description: "Metadata only, newest first (ADR-0053). An instance admin sees everything; with project, an admin of that project sees its events and those of its descendants.",
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *listAuditInput) (*listAuditOutput, error) {
		if err := d.requireToReadAudit(ctx, in.Project); err != nil {
			return nil, apiError(ctx, err)
		}
		p, _ := PrincipalFrom(ctx)
		// Secret names, fields and refs (including node credentials) are for instance admins.
		f := store.AuditFilter{TargetType: in.TargetType, Target: in.Target, Project: in.Project, HideSecrets: !p.InstanceAdmin}
		cur, err := decodeCursor(ctx, in.Cursor)
		if err != nil {
			return nil, err
		}
		if cur != "" {
			atStr, id, ok := strings.Cut(cur, "\x00")
			at, perr := time.Parse(time.RFC3339Nano, atStr)
			if !ok || perr != nil || id == "" {
				return nil, newHumaError(http.StatusBadRequest, "invalid cursor")
			}
			f.BeforeAt, f.BeforeID = at, id
		}
		rows, err := d.ds.ListAuditEvents(ctx, f, in.Limit+1)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		out := &listAuditOutput{}
		out.Body.Items = []AuditEvent{}
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			last := rows[len(rows)-1]
			out.Body.NextCursor = encodeCursor(last.At.UTC().Format(time.RFC3339Nano) + "\x00" + last.ID)
		}
		for _, e := range rows {
			details := e.Details
			if len(details) == 0 {
				details = json.RawMessage("{}")
			}
			out.Body.Items = append(out.Body.Items, AuditEvent{
				ID: e.ID, At: e.At, Actor: e.Actor, Action: e.Action, TargetType: e.TargetType, Target: e.Target,
				ProjectPath: e.ProjectPath, Details: details, RequestID: e.RequestID, ActorName: e.ActorName,
			})
		}
		return out, nil
	})
}

// requireToReadAudit lets an instance admin read every event, and an admin of a
// project the events of that project and its descendants. Anyone else gets 403
// without a project, and 404 for a project they hold no role on.
func (d deps) requireToReadAudit(ctx context.Context, project string) error {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return errForbidden
	}
	if p.InstanceAdmin {
		return nil
	}
	if project == "" {
		return errForbidden
	}
	if store.ValidateProjectPath(project) != nil {
		return store.ErrNotFound
	}
	return d.require(ctx, project, "", store.RoleAdmin)
}
