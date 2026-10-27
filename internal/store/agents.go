package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// NewID returns a new UUIDv7 string, the id format of every store-generated
// row. Ids are generated in Go, never by the database.
func NewID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generating id: %w", err)
	}
	return id.String(), nil
}

// EncodeJSON marshals v for a JSON column. JSON handling stays in Go, never
// in SQL.
func EncodeJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding json column: %w", err)
	}
	return b, nil
}

// EncodeStringMap marshals a string map, encoding nil as {}.
func EncodeStringMap(m map[string]string) ([]byte, error) {
	if m == nil {
		m = map[string]string{}
	}
	return EncodeJSON(m)
}

// ObjectOrEmpty returns raw, or {} when raw is empty.
func ObjectOrEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

// Page size bounds for ListAgents.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 500
)

// ClampLimit maps a requested page size into [1, MaxPageLimit]; <= 0 means the default.
func ClampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultPageLimit
	case n > MaxPageLimit:
		return MaxPageLimit
	}
	return n
}

// DecodeJSON unmarshals a JSON column into v.
func DecodeJSON(b []byte, v any) error {
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decoding json column: %w", err)
	}
	return nil
}

// Pool is an agent pool.
type Pool struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
}

// PoolToken describes a pool token. The hash is never returned; the
// plaintext is never stored (ARCHITECTURE section 7.1).
type PoolToken struct {
	ID         string
	PoolID     string
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// PoolStore persists agent pools and their tokens.
type PoolStore interface {
	// CreatePool returns ErrConflict if the name is taken.
	CreatePool(ctx context.Context, name, description string, now time.Time) (Pool, error)
	// GetPoolByName returns ErrNotFound if absent.
	GetPoolByName(ctx context.Context, name string) (Pool, error)
	ListPools(ctx context.Context) ([]Pool, error)

	// CreatePoolToken stores hash (hex SHA-256 of the token). It returns
	// ErrNotFound if the pool is absent and ErrConflict on a duplicate hash.
	CreatePoolToken(ctx context.Context, poolID, name, hash string, now time.Time) (PoolToken, error)
	// LookupPoolToken resolves a hash to its pool and token. It returns
	// ErrNotFound if the hash is unknown or the token is revoked.
	LookupPoolToken(ctx context.Context, hash string) (Pool, PoolToken, error)
	// TouchPoolToken records last use. It returns ErrNotFound if absent.
	TouchPoolToken(ctx context.Context, tokenID string, now time.Time) error
	// RevokePoolToken returns ErrNotFound if absent and ErrStale if it is
	// already revoked.
	RevokePoolToken(ctx context.Context, tokenID string, now time.Time) error
	// ListPoolTokens lists a pool's tokens (revoked included), oldest first.
	ListPoolTokens(ctx context.Context, poolID string) ([]PoolToken, error)
}

// Agent is a connected (or formerly connected) worker.
type Agent struct {
	ID             string
	PoolID         string
	Version        string
	Protocol       int
	Labels         map[string]string
	Capabilities   map[string]string // plugin name -> version
	Capacity       int
	Telemetry      json.RawMessage // last snapshot, opaque to the store
	ControllerID   string          // empty when disconnected
	ConnectedAt    time.Time
	LastSeen       time.Time
	DisconnectedAt *time.Time
}

// AgentHello is what an agent reports in its hello.
type AgentHello struct {
	ID           string
	PoolID       string
	Version      string
	Protocol     int
	Labels       map[string]string
	Capabilities map[string]string
	Capacity     int
	Telemetry    json.RawMessage // may be empty
	ControllerID string          // the controller accepting the socket
}

// AgentTouch is one batched heartbeat write.
type AgentTouch struct {
	ID        string
	LastSeen  time.Time
	Telemetry json.RawMessage
}

// AgentPage is one page of ListAgents. NextCursor is empty on the last page.
type AgentPage struct {
	Agents     []Agent
	NextCursor string
}

// AgentStore persists agents.
type AgentStore interface {
	// UpsertAgentOnHello inserts or replaces the agent row and makes
	// h.ControllerID its owner. The latest hello wins (a reconnect to another
	// controller takes ownership). It returns ErrNotFound if the pool is absent.
	UpsertAgentOnHello(ctx context.Context, h AgentHello, now time.Time) error
	// BatchTouchAgents updates last_seen and telemetry in one transaction,
	// only for agents still owned by controllerID. It returns how many rows
	// were applied; agents owned elsewhere are skipped, not errors.
	BatchTouchAgents(ctx context.Context, controllerID string, touches []AgentTouch) (int, error)
	// MarkAgentDisconnected is a CAS on controller_id. It returns ErrNotFound
	// if the agent is absent and ErrStale if controllerID no longer owns it.
	MarkAgentDisconnected(ctx context.Context, agentID, controllerID string, now time.Time) error
	// GetAgent returns ErrNotFound if absent.
	GetAgent(ctx context.Context, id string) (Agent, error)
	// ListAgents is keyset-paginated by id. cursor is "" for the first page.
	ListAgents(ctx context.Context, cursor string, limit int) (AgentPage, error)
}

// ControllerInstance is a registered controller (ARCHITECTURE section 8.5).
type ControllerInstance struct {
	ID          string
	InternalURL string
	StartedAt   time.Time
	LastSeen    time.Time
}

// ControllerStore persists the controller registry.
type ControllerStore interface {
	// RegisterControllerInstance inserts or resets the row (started_at = last_seen = now).
	RegisterControllerInstance(ctx context.Context, id, internalURL string, now time.Time) error
	// HeartbeatControllerInstance returns ErrNotFound if absent.
	HeartbeatControllerInstance(ctx context.Context, id string, now time.Time) error
	// DeleteControllerInstance returns ErrNotFound if absent.
	DeleteControllerInstance(ctx context.Context, id string) error
	ListControllerInstances(ctx context.Context) ([]ControllerInstance, error)
}
