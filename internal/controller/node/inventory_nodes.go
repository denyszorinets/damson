package node

import (
	"context"
	"errors"
	"fmt"

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
}

// Node implements dispatch.NodeSource. A missing node and a node outside the
// project's scope both read as unknown, so a job cannot probe the inventory.
func (s *InventoryNodes) Node(ctx context.Context, runID, name string) (pluginsdk.NodeInfo, error) {
	n, err := s.lookup(ctx, runID, name)
	if errors.Is(err, store.ErrNotFound) {
		return pluginsdk.NodeInfo{}, dispatch.ErrUnknownNode
	}
	if err != nil {
		return pluginsdk.NodeInfo{}, err
	}
	project, err := s.Project(ctx, runID)
	if err != nil {
		return pluginsdk.NodeInfo{}, fmt.Errorf("resolving the project of run %q: %w", runID, err)
	}
	ok, err := s.Scope.Allows(ctx, project, n)
	if err != nil {
		return pluginsdk.NodeInfo{}, err
	}
	if !ok {
		return pluginsdk.NodeInfo{}, dispatch.ErrUnknownNode
	}
	if !n.Enabled {
		return pluginsdk.NodeInfo{}, dispatch.ErrNodeDisabled
	}
	// Fail closed until host key handling exists (#118): the controller does not
	// store observed keys yet, so tofu without a pin would trust any key on every
	// connection, and insecure is only allowed when explicitly enabled.
	if n.HostKeyPolicy == inventory.PolicyInsecure || (n.HostKeyPolicy == inventory.PolicyTOFU && n.HostKey == "") {
		return pluginsdk.NodeInfo{}, dispatch.ErrHostKeyPolicy
	}
	return pluginsdk.NodeInfo{
		Name: name, Address: n.Address, Port: n.Port, Transport: n.Transport, OS: n.OS, Attributes: n.Attributes,
		Credential: n.Credential, HostKey: n.HostKey, HostKeyPolicy: n.HostKeyPolicy,
	}, nil
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
