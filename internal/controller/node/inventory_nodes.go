package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// nodeAndTargets is the part of the store InventoryNodes reads.
type nodeAndTargets interface {
	store.NodeStore
	store.RunTargetStore
}

// InventoryNodes serves the dispatcher from the inventory. It reads the node row
// when a step is dispatched, so address, credential, host key and policy are
// those of that moment; a node deleted since the run started fails the step
// for good.
type InventoryNodes struct {
	Store   nodeAndTargets
	Scope   inventory.Scope
	Project func(ctx context.Context, runID string) (string, error)
	// Now is the clock for observed keys; default time.Now.
	Now func() time.Time
	// Log receives the warning for a conflicting first key; optional.
	Log *slog.Logger
}

// admit finds the node a step names and applies the scope and enabled checks,
// returning dispatch's permanent errors. Dispatch and host key recording both
// go through it.
func (s *InventoryNodes) admit(ctx context.Context, runID, name string) (store.Node, error) {
	n, err := s.lookup(ctx, runID, name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Node{}, dispatch.ErrUnknownNode
	}
	if err != nil {
		return store.Node{}, err
	}
	project, err := s.Project(ctx, runID)
	if err != nil {
		return store.Node{}, fmt.Errorf("resolving the project of run %q: %w", runID, err)
	}
	ok, err := s.Scope.Allows(ctx, project, n)
	if err != nil {
		return store.Node{}, err
	}
	if !ok {
		return store.Node{}, dispatch.ErrUnknownNode
	}
	if !n.Enabled {
		return store.Node{}, dispatch.ErrNodeDisabled
	}
	return n, nil
}

// Node implements dispatch.NodeSource. A missing node and a node outside the
// project's scope both read as unknown, so a job cannot probe the inventory.
func (s *InventoryNodes) Node(ctx context.Context, runID, name string) (pluginsdk.NodeInfo, error) {
	n, err := s.admit(ctx, runID, name)
	if err != nil {
		return pluginsdk.NodeInfo{}, err
	}
	// The controller never dispatches the insecure policy (the agent's opt-in
	// stays as a second gate). A tofu node is held to the key it was pinned
	// with, else to the first key observed; with neither, the agent accepts
	// the first key and reports it (ARCHITECTURE 6.1).
	if n.HostKeyPolicy == inventory.PolicyInsecure {
		return pluginsdk.NodeInfo{}, dispatch.ErrHostKeyPolicy
	}
	key := n.HostKey
	if key == "" && n.HostKeyPolicy == inventory.PolicyTOFU {
		key = n.HostKeyObserved
	}
	return pluginsdk.NodeInfo{
		Name: name, Address: n.Address, Port: n.Port, Transport: n.Transport, OS: n.OS, Attributes: n.Attributes,
		Credential: n.Credential, HostKey: key, HostKeyPolicy: n.HostKeyPolicy,
	}, nil
}

// NodePool implements dispatch.PoolSource: the pool of the node a step names,
// with the same scope, enabled and host key policy checks as Node.
func (s *InventoryNodes) NodePool(ctx context.Context, runID, name string) (string, error) {
	n, err := s.admit(ctx, runID, name)
	if err != nil {
		return "", err
	}
	if n.HostKeyPolicy == inventory.PolicyInsecure {
		return "", dispatch.ErrHostKeyPolicy
	}
	return n.Pool, nil
}

// lookup finds the node a step names. A node frozen into the run's targets is
// followed by id, so a rename after the run started still reaches it (and a
// delete reads as unknown); a name outside the snapshot is looked up by name.
func (s *InventoryNodes) lookup(ctx context.Context, runID, name string) (store.Node, error) {
	id, err := s.Store.RunTargetNodeID(ctx, runID, name)
	if errors.Is(err, store.ErrNotFound) {
		return s.Store.GetNode(ctx, name)
	}
	if err != nil {
		return store.Node{}, err
	}
	n, err := s.Store.GetNodeByID(ctx, id)
	if err != nil {
		return store.Node{}, err
	}
	return n, nil
}

// ObserveHostKey implements dispatch.HostKeyRecorder. The node must be one the
// run may dispatch to (scope, enabled), a tofu node without a pinned key. The
// write is fenced on the attempt and never overwrites; when it loses to a
// different key, that is logged: two first connections saw different keys.
func (s *InventoryNodes) ObserveHostKey(ctx context.Context, runID, attemptID, agentID, name, key string) error {
	n, err := s.admit(ctx, runID, name)
	if errors.Is(err, dispatch.ErrUnknownNode) || errors.Is(err, dispatch.ErrNodeDisabled) {
		return nil
	}
	if err != nil {
		return err
	}
	if n.HostKeyPolicy != inventory.PolicyTOFU || n.HostKey != "" {
		return nil
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	ok, err := s.Store.ObserveNodeHostKey(ctx, n.ID, key, attemptID, agentID, now())
	if err != nil || ok || s.Log == nil {
		return err
	}
	if cur, gerr := s.Store.GetNodeByID(ctx, n.ID); gerr == nil && cur.HostKeyObserved != "" && cur.HostKeyObserved != key {
		s.Log.Warn("a tofu node showed a different host key than the one recorded first",
			"run_id", runID, "attempt_id", attemptID, "agent_id", agentID, "node", name,
			"recorded", inventory.Fingerprint(cur.HostKeyObserved), "seen", inventory.Fingerprint(key))
	}
	return nil
}
