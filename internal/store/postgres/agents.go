package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

var (
	_ store.AgentStore      = (*Store)(nil)
	_ store.ControllerStore = (*Store)(nil)
)

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// UpsertAgentOnHello implements store.AgentStore.
func (s *Store) UpsertAgentOnHello(ctx context.Context, h store.AgentHello, now time.Time) error {
	labels, err := store.EncodeStringMap(h.Labels)
	if err != nil {
		return err
	}
	caps, err := store.EncodeStringMap(h.Capabilities)
	if err != nil {
		return err
	}
	err = s.q.UpsertAgent(ctx, gen.UpsertAgentParams{
		ID: h.ID, PoolID: h.PoolID, Version: h.Version, Protocol: int32(h.Protocol),
		Labels: labels, Capabilities: caps, Capacity: int32(h.Capacity),
		Telemetry: store.ObjectOrEmpty(h.Telemetry), ControllerID: text(h.ControllerID),
		ConnectedAt: ts(now), LastSeen: ts(now),
	})
	if hasCode(err, foreignKeyViolation) {
		return fmt.Errorf("pool %q: %w", h.PoolID, store.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("upserting agent %q: %w", h.ID, err)
	}
	return nil
}

// BatchTouchAgents implements store.AgentStore.
func (s *Store) BatchTouchAgents(ctx context.Context, controllerID string, touches []store.AgentTouch) (int, error) {
	if len(touches) == 0 {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning touch batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	applied := 0
	for _, t := range touches {
		n, err := q.TouchAgentCAS(ctx, gen.TouchAgentCASParams{
			LastSeen: ts(t.LastSeen), Telemetry: store.ObjectOrEmpty(t.Telemetry),
			ID: t.ID, ControllerID: text(controllerID),
		})
		if err != nil {
			return 0, fmt.Errorf("touching agent %q: %w", t.ID, err)
		}
		applied += int(n)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing touch batch: %w", err)
	}
	return applied, nil
}

// MarkAgentDisconnected implements store.AgentStore.
func (s *Store) MarkAgentDisconnected(ctx context.Context, agentID, controllerID string, now time.Time) error {
	n, err := s.q.DisconnectAgentCAS(ctx, gen.DisconnectAgentCASParams{
		DisconnectedAt: ts(now), ID: agentID, ControllerID: text(controllerID),
	})
	if err != nil {
		return fmt.Errorf("disconnecting agent %q: %w", agentID, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := s.GetAgent(ctx, agentID); err != nil {
		return err
	}
	return fmt.Errorf("agent %q not owned by controller %q: %w", agentID, controllerID, store.ErrStale)
}

// GetAgent implements store.AgentStore.
func (s *Store) GetAgent(ctx context.Context, id string) (store.Agent, error) {
	row, err := s.q.GetAgent(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Agent{}, fmt.Errorf("agent %q: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Agent{}, fmt.Errorf("getting agent %q: %w", id, err)
	}
	return toAgent(row)
}

// ListAgents implements store.AgentStore.
func (s *Store) ListAgents(ctx context.Context, cursor string, limit int) (store.AgentPage, error) {
	limit = store.ClampLimit(limit)
	rows, err := s.q.ListAgentsAfter(ctx, gen.ListAgentsAfterParams{AfterID: cursor, RowLimit: int32(limit) + 1})
	if err != nil {
		return store.AgentPage{}, fmt.Errorf("listing agents: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	page := store.AgentPage{Agents: make([]store.Agent, 0, len(rows))}
	for _, r := range rows {
		a, err := toAgent(r)
		if err != nil {
			return store.AgentPage{}, err
		}
		page.Agents = append(page.Agents, a)
	}
	if more {
		page.NextCursor = page.Agents[len(page.Agents)-1].ID
	}
	return page, nil
}

func toAgent(r gen.Agent) (store.Agent, error) {
	a := store.Agent{
		ID: r.ID, PoolID: r.PoolID, Version: r.Version, Protocol: int(r.Protocol),
		Capacity: int(r.Capacity), Telemetry: r.Telemetry, ControllerID: r.ControllerID.String,
		ConnectedAt: r.ConnectedAt.Time.UTC(), LastSeen: r.LastSeen.Time.UTC(),
		DisconnectedAt: tsPtr(r.DisconnectedAt),
	}
	if err := store.DecodeJSON(r.Labels, &a.Labels); err != nil {
		return store.Agent{}, err
	}
	if err := store.DecodeJSON(r.Capabilities, &a.Capabilities); err != nil {
		return store.Agent{}, err
	}
	return a, nil
}

// RegisterControllerInstance implements store.ControllerStore.
func (s *Store) RegisterControllerInstance(ctx context.Context, id, internalURL string, now time.Time) error {
	err := s.q.UpsertControllerInstance(ctx, gen.UpsertControllerInstanceParams{
		ID: id, InternalUrl: internalURL, StartedAt: ts(now), LastSeen: ts(now),
	})
	if err != nil {
		return fmt.Errorf("registering controller %q: %w", id, err)
	}
	return nil
}

// HeartbeatControllerInstance implements store.ControllerStore.
func (s *Store) HeartbeatControllerInstance(ctx context.Context, id string, now time.Time) error {
	n, err := s.q.HeartbeatControllerInstance(ctx, gen.HeartbeatControllerInstanceParams{LastSeen: ts(now), ID: id})
	if err != nil {
		return fmt.Errorf("heartbeating controller %q: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("controller %q: %w", id, store.ErrNotFound)
	}
	return nil
}

// DeleteControllerInstance implements store.ControllerStore.
func (s *Store) DeleteControllerInstance(ctx context.Context, id string) error {
	n, err := s.q.DeleteControllerInstance(ctx, id)
	if err != nil {
		return fmt.Errorf("deleting controller %q: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("controller %q: %w", id, store.ErrNotFound)
	}
	return nil
}

// ListControllerInstances implements store.ControllerStore.
func (s *Store) ListControllerInstances(ctx context.Context) ([]store.ControllerInstance, error) {
	rows, err := s.q.ListControllerInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing controllers: %w", err)
	}
	out := make([]store.ControllerInstance, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.ControllerInstance{
			ID: r.ID, InternalURL: r.InternalUrl, StartedAt: r.StartedAt.Time.UTC(), LastSeen: r.LastSeen.Time.UTC(),
		})
	}
	return out, nil
}
