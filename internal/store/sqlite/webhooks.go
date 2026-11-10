package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

var _ store.WebhookStore = (*Store)(nil)

func toWebhook(r gen.Webhook) (store.Webhook, error) {
	revoked, err := parseNullTime(r.RevokedAt)
	if err != nil {
		return store.Webhook{}, err
	}
	used, err := parseNullTime(r.LastUsedAt)
	if err != nil {
		return store.Webhook{}, err
	}
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return store.Webhook{}, err
	}
	updated, err := parseTime(r.UpdatedAt)
	if err != nil {
		return store.Webhook{}, err
	}
	return store.Webhook{
		ID: r.ID, JobID: r.JobID, Name: r.Name, Options: json.RawMessage(r.Options), OptionsMapping: r.OptionsMapping,
		RevokedAt: revoked, LastUsedAt: used, Version: r.Version, CreatedAt: created, UpdatedAt: updated,
	}, nil
}

// CreateWebhook implements store.WebhookStore.
func (s *Store) CreateWebhook(ctx context.Context, n store.NewWebhook, now time.Time) (store.Webhook, error) {
	if err := n.Validate(); err != nil {
		return store.Webhook{}, err
	}
	n.Version, n.CreatedAt, n.UpdatedAt = 1, store.NormalizeTime(now), store.NormalizeTime(now)
	ev, err := store.NewWebhookEvent(ctx, store.ActionWebhookCreate, n.Webhook, now)
	if err != nil {
		return store.Webhook{}, err
	}
	err = s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		if _, err := getJob(ctx, q, n.JobID); err != nil {
			return err
		}
		err := q.InsertWebhook(ctx, gen.InsertWebhookParams{
			ID: n.ID, JobID: n.JobID, Name: n.Name, TokenHash: n.TokenHash, Options: optionsText(n.Options),
			OptionsMapping: n.OptionsMapping, CreatedAt: fmtTime(now), UpdatedAt: fmtTime(now),
		})
		if isUnique(err) {
			return fmt.Errorf("webhook %q of job %q: %w", n.Name, n.JobID, store.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("creating webhook: %w", err)
		}
		if err := fillJobProject(ctx, tx.(*txn).q, &ev, n.JobID); err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
	if err != nil {
		return store.Webhook{}, err
	}
	n.Options = json.RawMessage(optionsText(n.Options))
	return n.Webhook, nil
}

// GetWebhook implements store.WebhookStore.
func (s *Store) GetWebhook(ctx context.Context, id string) (store.Webhook, error) {
	r, err := s.q.GetWebhook(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Webhook{}, fmt.Errorf("webhook %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Webhook{}, fmt.Errorf("reading webhook: %w", err)
	}
	return toWebhook(r)
}

// ListWebhooks implements store.WebhookStore.
func (s *Store) ListWebhooks(ctx context.Context, jobID, afterID string, limit int) ([]store.Webhook, error) {
	rows, err := s.q.ListWebhooks(ctx, gen.ListWebhooksParams{JobID: jobID, AfterID: afterID, RowLimit: int64(store.ClampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("listing webhooks: %w", err)
	}
	out := make([]store.Webhook, len(rows))
	for i, r := range rows {
		if out[i], err = toWebhook(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// FindWebhook implements store.WebhookStore.
func (s *Store) FindWebhook(ctx context.Context, tokenHash string) (store.Webhook, error) {
	r, err := s.q.GetActiveWebhookByTokenHash(ctx, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Webhook{}, store.ErrNotFound // no token hash in the message
	}
	if err != nil {
		return store.Webhook{}, fmt.Errorf("finding webhook: %w", err)
	}
	return toWebhook(r)
}

// RevokeWebhook implements store.WebhookStore.
func (s *Store) RevokeWebhook(ctx context.Context, id string, now time.Time) error {
	if _, err := store.AuditFrom(ctx); err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		cur, err := q.GetWebhook(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("webhook %q: %w", id, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading webhook: %w", err)
		}
		n, err := q.RevokeWebhook(ctx, gen.RevokeWebhookParams{RevokedAt: someTime(now), ID: id})
		if err != nil {
			return fmt.Errorf("revoking webhook: %w", err)
		}
		if n == 0 {
			return nil // already revoked
		}
		old, err := toWebhook(cur)
		if err != nil {
			return err
		}
		ev, err := store.NewWebhookEvent(ctx, store.ActionWebhookRevoke, old, now)
		if err != nil {
			return err
		}
		if err := fillJobProject(ctx, tx.(*txn).q, &ev, old.JobID); err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
}

// TouchWebhook implements store.Tx.
func (t *txn) TouchWebhook(ctx context.Context, id string, now time.Time) error {
	n, err := t.q.TouchWebhook(ctx, gen.TouchWebhookParams{Now: someTime(now), ID: id, OlderThan: someTime(now.Add(-store.WebhookTouchWindow))})
	if err != nil {
		return fmt.Errorf("touching webhook: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("webhook %q is revoked or gone: %w", id, store.ErrNotFound)
	}
	return nil
}

// ListWebhooksByProject implements store.WebhookStore.
func (s *Store) ListWebhooksByProject(ctx context.Context, projectPath, afterID string, limit int) ([]store.Webhook, error) {
	rows, err := s.q.ListWebhooksByProject(ctx, gen.ListWebhooksByProjectParams{ProjectPath: projectPath, AfterID: afterID, RowLimit: int64(store.ClampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("listing webhooks of project %s: %w", projectPath, err)
	}
	out := make([]store.Webhook, len(rows))
	for i, r := range rows {
		if out[i], err = toWebhook(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}
