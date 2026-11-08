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
	Actor       string          `json:"actor" doc:"admin-token:<hash prefix>, cli:<user> or system:<duty> until accounts arrive (ADR-0053)."`
	Action      string          `json:"action" doc:"secret.create, secret.update, secret.delete, secret.kek_rotate, log.purge, admin_token.create, admin_token.revoke, audit.prune, schedule.create, schedule.update, schedule.delete, webhook.create, webhook.revoke, project.create, project.update or project.delete."`
	TargetType  string          `json:"target_type" doc:"secret, execution, schedule, webhook or instance."`
	Target      string          `json:"target" doc:"<scope>/<name> for a secret, the execution id for log.purge."`
	ProjectPath string          `json:"project_path,omitempty"`
	Details     json.RawMessage `json:"details" doc:"A JSON object; never a secret value."`
	RequestID   string          `json:"request_id,omitempty"`
}

type listAuditInput struct {
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
		Objects int64 `json:"objects" doc:"Stored log objects removed."`
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
		out.Body.Chunks, out.Body.Objects = res.Chunks, res.Objects
		return out, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "list-audit-events", Method: http.MethodGet, Path: "/audit",
		Summary: "List audit events", Tags: []string{"audit"}, Security: adminSecurity,
		Middlewares: huma.Middlewares{requireAdmin(api, d)},
		Description: "Secret writes today (ADR-0053). Metadata only, newest first.",
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *listAuditInput) (*listAuditOutput, error) {
		f := store.AuditFilter{TargetType: in.TargetType, Target: in.Target}
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
				ProjectPath: e.ProjectPath, Details: details, RequestID: e.RequestID,
			})
		}
		return out, nil
	})
}
