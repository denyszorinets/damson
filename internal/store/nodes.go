package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Node is an inventory entry (ARCHITECTURE 11): an execution target. Source
// and ExternalID say who owns it ("manual" for the API, a sync source name
// otherwise). Version is the CAS counter of everything but the observed host
// key, which the TOFU flow writes without bumping it.
type Node struct {
	ID, Name, Address string
	Port              int
	Transport, OS     string
	Pool              string
	Tags              []string
	Attributes        map[string]any
	Credential        string // a secret:// ref, or ""
	HostKey           string // pinned key in authorized_keys form, or ""
	HostKeyPolicy     string
	HostKeyObserved   string
	HostKeyObservedAt *time.Time
	Source            string
	ExternalID        string
	Enabled           bool
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NodeFilter selects a page of nodes ordered by name.
type NodeFilter struct {
	Source    string // empty means every source
	Enabled   *bool  // nil means both
	AfterName string // keyset cursor: names greater than this
	Limit     int    // clamped by ClampNodeLimit
}

// ClampNodeLimit turns a requested page size into one both databases accept:
// zero or negative means the default, and the maximum bounds a page.
func ClampNodeLimit(n int) int {
	switch {
	case n < 1:
		return 50
	case n > 1000:
		return 1000
	}
	return n
}

// NodeStore persists the inventory. Validation is the caller's job
// (internal/inventory); the store enforces uniqueness and CAS.
type NodeStore interface {
	// CreateNode inserts n at version 1. ErrConflict if the name (also
	// compared case-insensitively) or (source, external_id) exists.
	CreateNode(ctx context.Context, n Node, now time.Time) (Node, error)
	// GetNode returns ErrNotFound if absent.
	GetNode(ctx context.Context, name string) (Node, error)
	// GetNodeByID returns ErrNotFound if absent.
	GetNodeByID(ctx context.Context, id string) (Node, error)
	// FindNodeByName is GetNode ignoring case, as the unique index does.
	FindNodeByName(ctx context.Context, name string) (Node, error)
	// ListNodes pages by name.
	ListNodes(ctx context.Context, f NodeFilter) ([]Node, error)
	// UpdateNode replaces the mutable fields of the node with n.ID if its
	// version is expected, bumping it. It never writes name, source,
	// external_id or the observed host key. ErrNotFound if the id is gone,
	// ErrStale if the version moved.
	UpdateNode(ctx context.Context, n Node, expected int64, now time.Time) (Node, error)
	// ObserveNodeHostKey records the key a tofu node showed first. It writes
	// only if the node is tofu, has no pinned key and no observed key yet, and
	// reports whether it did (false: nothing changed, including a missing
	// node). It never overwrites a key and does not bump the version.
	// The write is fenced: attemptID must be an unfinished attempt owned by
	// agentID (checked in the same statement).
	ObserveNodeHostKey(ctx context.Context, id, key, attemptID, agentID string, now time.Time) (bool, error)
	// ClearNodeHostKeyObserved forgets the observed key (the next tofu
	// connection observes again). ErrNotFound if the id is gone.
	ClearNodeHostKeyObserved(ctx context.Context, id string) error
	// DeleteNode removes the node with that id if its version is expected.
	// ErrNotFound / ErrStale as for UpdateNode.
	DeleteNode(ctx context.Context, id string, expected int64) error
	// ApplyNodeDiff applies d in one transaction: deletes, then updates, then
	// creates (so a name freed by a delete can be reused). Every update and
	// delete is a CAS on id and version. ErrStale if a node moved or is gone,
	// ErrConflict on a name or (source, external_id) clash; either rolls the
	// whole diff back.
	ApplyNodeDiff(ctx context.Context, d NodeDiff, now time.Time) error
}

// NodeUpdate replaces everything a source describes about a node (also its
// name and external id) if the node is still at Expected.
type NodeUpdate struct {
	Node     Node
	Expected int64
}

// NodeDelete removes a node if it is still at Expected.
type NodeDelete struct {
	ID       string
	Expected int64
}

// NodeDiff is a set of changes applied together.
type NodeDiff struct {
	Create []Node
	Update []NodeUpdate
	Delete []NodeDelete
}

// EncodeTags renders tags as the JSON array stored in the nodes table.
func EncodeTags(tags []string) (string, error) {
	if tags == nil {
		tags = []string{}
	}
	b, err := json.Marshal(tags)
	return string(b), err
}

// DecodeTags is the inverse of EncodeTags.
func DecodeTags(s string) ([]string, error) {
	var tags []string
	if err := json.Unmarshal([]byte(s), &tags); err != nil {
		return nil, fmt.Errorf("decoding node tags: %w", err)
	}
	if tags == nil {
		tags = []string{}
	}
	return tags, nil
}

// EncodeAttributes renders attributes as the JSON object stored in the table.
func EncodeAttributes(attrs map[string]any) (string, error) {
	if attrs == nil {
		attrs = map[string]any{}
	}
	b, err := json.Marshal(attrs)
	return string(b), err
}

// DecodeAttributes is the inverse of EncodeAttributes. Numbers decode as float64.
func DecodeAttributes(s string) (map[string]any, error) {
	attrs := map[string]any{}
	if err := json.Unmarshal([]byte(s), &attrs); err != nil {
		return nil, fmt.Errorf("decoding node attributes: %w", err)
	}
	return attrs, nil
}
