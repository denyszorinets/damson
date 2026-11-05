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

// InventoryNodes serves the dispatcher from the inventory. It reads the node row
// when a step is dispatched, so address, credential, host key and policy are
// those of that moment; a node deleted since the run started fails the step
// for good.
type InventoryNodes struct {
	Store   store.NodeStore
	Scope   inventory.Scope
	Project func(ctx context.Context, runID string) (string, error)
}

// Node implements dispatch.NodeSource. A missing node and a node outside the
// project's scope both read as unknown, so a job cannot probe the inventory.
func (s *InventoryNodes) Node(ctx context.Context, runID, name string) (pluginsdk.NodeInfo, error) {
	n, err := s.Store.GetNode(ctx, name)
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
		Name: n.Name, Address: n.Address, Port: n.Port, Transport: n.Transport, OS: n.OS, Attributes: n.Attributes,
		Credential: n.Credential, HostKey: n.HostKey, HostKeyPolicy: n.HostKeyPolicy,
	}, nil
}
