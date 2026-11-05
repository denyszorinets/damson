package node

import (
	"context"
	"fmt"

	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/store"
)

// maxTargets bounds the list a run can see (ADR-0038 lane limit).
const maxTargets = 1000

// InventoryTargets lists the enabled nodes a project's scope allows, sorted by
// name, as the plain maps `targets` and the selector read. A map never carries
// the credential, the host key or its policy. Until runs freeze their targets
// at start (#117) the list is read live, when the engine needs it.
type InventoryTargets struct {
	Store store.NodeStore
	Scope inventory.Scope
}

// Targets implements api.TargetSource and advance.TargetSource.
func (t *InventoryTargets) Targets(ctx context.Context, project string) ([]map[string]any, error) {
	enabled := true
	var out []map[string]any
	after := ""
	for len(out) < maxTargets {
		page, err := t.Store.ListNodes(ctx, store.NodeFilter{Enabled: &enabled, AfterName: after, Limit: 200})
		if err != nil {
			return nil, fmt.Errorf("listing nodes: %w", err)
		}
		for _, n := range page {
			ok, err := t.Scope.Allows(ctx, project, n)
			if err != nil {
				return nil, fmt.Errorf("checking the node scope: %w", err)
			}
			if ok && len(out) < maxTargets {
				out = append(out, targetOf(n))
			}
		}
		if len(page) < 200 {
			break
		}
		after = page[len(page)-1].Name
	}
	return out, nil
}

func targetOf(n store.Node) map[string]any {
	tags := make([]any, len(n.Tags))
	for i, t := range n.Tags {
		tags[i] = t
	}
	attrs := make(map[string]any, len(n.Attributes))
	for k, v := range n.Attributes {
		attrs[k] = v
	}
	return map[string]any{
		"name": n.Name, "address": n.Address, "tags": tags, "attributes": attrs,
		"pool": n.Pool, "os": n.OS, "transport": n.Transport,
	}
}
