package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

var _ store.AuditStore = (*Store)(nil)

// farFuture is the "before" of a first page.
var farFuture = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

func insertAudit(ctx context.Context, q *gen.Queries, ev store.AuditEvent) error {
	if err := store.ValidateAuditEvent(ev); err != nil {
		return err
	}
	details := string(ev.Details)
	if details == "" {
		details = "{}"
	}
	if err := q.InsertAuditEvent(ctx, gen.InsertAuditEventParams{
		ID: ev.ID, At: ts(ev.At), Actor: ev.Actor, Action: ev.Action, TargetType: ev.TargetType, Target: ev.Target,
		ProjectPath: ev.ProjectPath, Details: details, RequestID: ev.RequestID, ActorName: store.EventActorName(ctx, ev),
	}); err != nil {
		return fmt.Errorf("writing audit event: %w", err)
	}
	return nil
}

// AppendAuditEvent implements store.AuditStore.
func (s *Store) AppendAuditEvent(ctx context.Context, ev store.AuditEvent) error {
	return insertAudit(ctx, s.q, ev)
}

// AppendAuditEvent implements store.Tx.
func (t *txn) AppendAuditEvent(ctx context.Context, ev store.AuditEvent) error {
	return insertAudit(ctx, t.q, ev)
}

// ListAuditEvents implements store.AuditStore.
func (s *Store) ListAuditEvents(ctx context.Context, f store.AuditFilter, limit int) ([]store.AuditEvent, error) {
	before := f.BeforeAt
	if before.IsZero() {
		before = farFuture
	}
	from, to := f.From, f.To
	if from.IsZero() {
		from = time.Time{}.UTC()
	}
	if to.IsZero() {
		to = farFuture
	}
	rows, err := s.q.ListAuditEvents(ctx, gen.ListAuditEventsParams{
		Action: f.Action, Actor: f.Actor, HideSystem: hideFlag(f.HideSystem), FromAt: ts(from), ToAt: ts(to),
		HideSecrets: hideFlag(f.HideSecrets), Project: f.Project, ProjectSlash: f.Project + "/", ProjectNext: f.Project + "0", TargetType: f.TargetType, Target: f.Target, BeforeAt: ts(before), BeforeID: f.BeforeID, RowLimit: int32(store.ClampLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("listing audit events: %w", err)
	}
	out := make([]store.AuditEvent, len(rows))
	for i, r := range rows {
		out[i] = store.AuditEvent{
			ID: r.ID, At: r.At.Time.UTC(), Actor: r.Actor, Action: r.Action, TargetType: r.TargetType, Target: r.Target,
			ProjectPath: r.ProjectPath, Details: json.RawMessage(r.Details), RequestID: r.RequestID, ActorName: r.ActorName,
		}
	}
	return out, nil
}

// PruneAuditEvents implements store.Tx.
func (t *txn) PruneAuditEvents(ctx context.Context, before time.Time, limit int, now time.Time) (int64, error) {
	// A missing actor fails before anything is deleted.
	if _, err := store.AuditFrom(ctx); err != nil {
		return 0, err
	}
	n, err := t.q.PruneAuditEvents(ctx, gen.PruneAuditEventsParams{BeforeAt: ts(before), RowLimit: int32(store.ClampLimit(limit))})
	if err != nil {
		return 0, fmt.Errorf("pruning audit events: %w", err)
	}
	if n == 0 {
		return 0, nil
	}
	ev, err := store.NewAuditPruneEvent(ctx, store.AuditPruneDetails{Deleted: n, Before: before}, now)
	if err != nil {
		return 0, err
	}
	if err := insertAudit(ctx, t.q, ev); err != nil {
		return 0, err
	}
	return n, nil
}

// hideFlag is the query's text switch: non-empty hides secret.* events.
func hideFlag(hide bool) string {
	if hide {
		return "1"
	}
	return ""
}
