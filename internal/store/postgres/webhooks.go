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

var _ store.WebhookStore = (*Store)(nil)

func toWebhook(r gen.Webhook) store.Webhook {
	return store.Webhook{
		ID: r.ID, JobID: r.JobID, Name: r.Name, Options: json.RawMessage(r.Options), OptionsMapping: r.OptionsMapping,
		RevokedAt: tsPtr(r.RevokedAt), LastUsedAt: tsPtr(r.LastUsedAt), Version: r.Version,
		CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
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
			OptionsMapping: n.OptionsMapping, CreatedAt: ts(now), UpdatedAt: ts(now),
		})
		if isUniqueViolation(err) {
			return fmt.Errorf("webhook %q of job %q: %w", n.Name, n.JobID, store.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("creating webhook: %w", err)
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
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Webhook{}, fmt.Errorf("webhook %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Webhook{}, fmt.Errorf("reading webhook: %w", err)
	}
	return toWebhook(r), nil
}

// ListWebhooks implements store.WebhookStore.
func (s *Store) ListWebhooks(ctx context.Context, jobID, afterID string, limit int) ([]store.Webhook, error) {
	rows, err := s.q.ListWebhooks(ctx, gen.ListWebhooksParams{JobID: jobID, AfterID: afterID, RowLimit: int32(store.ClampLimit(limit))})
	if err != nil {
		return nil, fmt.Errorf("listing webhooks: %w", err)
	}
	out := make([]store.Webhook, len(rows))
	for i, r := range rows {
		out[i] = toWebhook(r)
	}
	return out, nil
}

// FindWebhook implements store.WebhookStore.
func (s *Store) FindWebhook(ctx context.Context, tokenHash string) (store.Webhook, error) {
	r, err := s.q.GetActiveWebhookByTokenHash(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Webhook{}, store.ErrNotFound // no token hash in the message
	}
	if err != nil {
		return store.Webhook{}, fmt.Errorf("finding webhook: %w", err)
	}
	return toWebhook(r), nil
}

// RevokeWebhook implements store.WebhookStore.
func (s *Store) RevokeWebhook(ctx context.Context, id string, now time.Time) error {
	if _, err := store.AuditFrom(ctx); err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx store.Tx) error {
		q := tx.(*txn).q
		cur, err := q.GetWebhook(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("webhook %q: %w", id, store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading webhook: %w", err)
		}
		n, err := q.RevokeWebhook(ctx, gen.RevokeWebhookParams{RevokedAt: ts(now), ID: id})
		if err != nil {
			return fmt.Errorf("revoking webhook: %w", err)
		}
		if n == 0 {
			return nil // already revoked
		}
		ev, err := store.NewWebhookEvent(ctx, store.ActionWebhookRevoke, toWebhook(cur), now)
		if err != nil {
			return err
		}
		return tx.AppendAuditEvent(ctx, ev)
	})
}

// TouchWebhook implements store.Tx.
func (t *txn) TouchWebhook(ctx context.Context, id string, now time.Time) error {
	if _, err := t.q.TouchWebhook(ctx, gen.TouchWebhookParams{Now: ts(now), ID: id, OlderThan: ts(now.Add(-store.WebhookTouchWindow))}); err != nil {
		return fmt.Errorf("touching webhook: %w", err)
	}
	return nil
}
