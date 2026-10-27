package store

import (
	"context"
	"encoding/json"
	"time"
)

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
	SessionSeq     uint64          // agent connection counter of the owning session
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
	SessionSeq   uint64          // per-agent-process connection counter from agent.hello
}

// AgentTouch is one batched heartbeat write.
type AgentTouch struct {
	ID         string
	SessionSeq uint64
	LastSeen   time.Time
	Telemetry  json.RawMessage
}

// AgentPage is one page of ListAgents. NextCursor is empty on the last page.
type AgentPage struct {
	Agents     []Agent
	NextCursor string
}

// AgentStore persists agents.
type AgentStore interface {
	// UpsertAgentOnHello inserts the agent row, or takes ownership of an
	// existing one. Ownership is ordered by h.SessionSeq, never by wall
	// clock: the takeover applies only if h.SessionSeq is higher than the
	// stored one. It returns ErrInvalid for an empty ControllerID or PoolID,
	// ErrNotFound if the pool is absent, ErrConflict if the agent exists in
	// a different pool, and ErrStale if the stored session is as new or newer.
	UpsertAgentOnHello(ctx context.Context, h AgentHello, now time.Time) error
	// BatchTouchAgents updates last_seen and telemetry in one transaction,
	// only for agents still owned by controllerID at the touch's SessionSeq
	// and whose stored last_seen is not newer than the touch (late batches
	// never move it backwards). It returns how many rows were applied; rows
	// that did not match are skipped, not errors.
	BatchTouchAgents(ctx context.Context, controllerID string, touches []AgentTouch) (int, error)
	// MarkAgentDisconnected is a CAS on (controller_id, session_seq). It
	// returns ErrNotFound if the agent is absent and ErrStale if the
	// controller/session no longer owns it.
	MarkAgentDisconnected(ctx context.Context, agentID, controllerID string, sessionSeq uint64, now time.Time) error
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
