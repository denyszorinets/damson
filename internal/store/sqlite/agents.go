package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite/gen"
)

var (
	_ store.AgentStore      = (*Store)(nil)
	_ store.ControllerStore = (*Store)(nil)
)

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// UpsertAgentOnHello implements store.AgentStore.
func (s *Store) UpsertAgentOnHello(ctx context.Context, h store.AgentHello, now time.Time) error {
	if err := store.ValidateHello(h); err != nil {
		return err
	}
	labels, err := store.EncodeStringMap(h.Labels)
	if err != nil {
		return err
	}
	caps, err := store.EncodeStringMap(h.Capabilities)
	if err != nil {
		return err
	}
	n, err := s.q.UpsertAgent(ctx, gen.UpsertAgentParams{
		ID: h.ID, PoolID: h.PoolID, Version: h.Version, Protocol: int64(h.Protocol),
		Labels: string(labels), Capabilities: string(caps), Capacity: int64(h.Capacity),
		Telemetry: string(store.ObjectOrEmpty(h.Telemetry)), ControllerID: nullStr(h.ControllerID),
		SessionSeq: int64(h.SessionSeq), ConnectedAt: fmtTime(now), LastSeen: fmtTime(now),
	})
	if isForeignKey(err) {
		return fmt.Errorf("pool %q: %w", h.PoolID, store.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("upserting agent %q: %w", h.ID, err)
	}
	if n > 0 {
		return nil
	}
	return s.explainRejectedHello(ctx, h)
}

// explainRejectedHello classifies a hello that changed no row.
func (s *Store) explainRejectedHello(ctx context.Context, h store.AgentHello) error {
	cur, err := s.GetAgent(ctx, h.ID)
	if err != nil {
		return err
	}
	if cur.PoolID != h.PoolID {
		return fmt.Errorf("agent %q belongs to another pool: %w", h.ID, store.ErrConflict)
	}
	return fmt.Errorf("agent %q hello session %d not newer than %d: %w", h.ID, h.SessionSeq, cur.SessionSeq, store.ErrStale)
}

// BatchTouchAgents implements store.AgentStore.
func (s *Store) BatchTouchAgents(ctx context.Context, controllerID string, touches []store.AgentTouch) (int, error) {
	if len(touches) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("beginning touch batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	applied := 0
	for _, t := range touches {
		n, err := q.TouchAgentCAS(ctx, gen.TouchAgentCASParams{
			LastSeen: fmtTime(t.LastSeen), Telemetry: string(store.ObjectOrEmpty(t.Telemetry)),
			ID: t.ID, ControllerID: nullStr(controllerID), SessionSeq: int64(t.SessionSeq),
		})
		if err != nil {
			return 0, fmt.Errorf("touching agent %q: %w", t.ID, err)
		}
		applied += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing touch batch: %w", err)
	}
	return applied, nil
}

// MarkAgentDisconnected implements store.AgentStore.
func (s *Store) MarkAgentDisconnected(ctx context.Context, agentID, controllerID string, sessionSeq uint64, now time.Time) error {
	n, err := s.q.DisconnectAgentCAS(ctx, gen.DisconnectAgentCASParams{
		DisconnectedAt: nullStr(fmtTime(now)), ID: agentID, ControllerID: nullStr(controllerID), SessionSeq: int64(sessionSeq),
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
	if errors.Is(err, sql.ErrNoRows) {
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
	rows, err := s.q.ListAgentsAfter(ctx, gen.ListAgentsAfterParams{AfterID: cursor, RowLimit: int64(limit) + 1})
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
		Capacity: int(r.Capacity), Telemetry: []byte(r.Telemetry), ControllerID: r.ControllerID.String, SessionSeq: uint64(r.SessionSeq),
	}
	if err := store.DecodeJSON([]byte(r.Labels), &a.Labels); err != nil {
		return store.Agent{}, err
	}
	if err := store.DecodeJSON([]byte(r.Capabilities), &a.Capabilities); err != nil {
		return store.Agent{}, err
	}
	var err error
	if a.ConnectedAt, err = parseTime(r.ConnectedAt); err != nil {
		return store.Agent{}, err
	}
	if a.LastSeen, err = parseTime(r.LastSeen); err != nil {
		return store.Agent{}, err
	}
	if a.DisconnectedAt, err = parseNullTime(r.DisconnectedAt); err != nil {
		return store.Agent{}, err
	}
	return a, nil
}

// RegisterControllerInstance implements store.ControllerStore.
func (s *Store) RegisterControllerInstance(ctx context.Context, id, internalURL string, now time.Time) error {
	err := s.q.UpsertControllerInstance(ctx, gen.UpsertControllerInstanceParams{
		ID: id, InternalUrl: internalURL, StartedAt: fmtTime(now), LastSeen: fmtTime(now),
	})
	if err != nil {
		return fmt.Errorf("registering controller %q: %w", id, err)
	}
	return nil
}

// HeartbeatControllerInstance implements store.ControllerStore.
func (s *Store) HeartbeatControllerInstance(ctx context.Context, id string, now time.Time) error {
	n, err := s.q.HeartbeatControllerInstance(ctx, gen.HeartbeatControllerInstanceParams{LastSeen: fmtTime(now), ID: id})
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
		started, err := parseTime(r.StartedAt)
		if err != nil {
			return nil, err
		}
		seen, err := parseTime(r.LastSeen)
		if err != nil {
			return nil, err
		}
		out = append(out, store.ControllerInstance{ID: r.ID, InternalURL: r.InternalUrl, StartedAt: started, LastSeen: seen})
	}
	return out, nil
}
