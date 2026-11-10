package node

import (
	"context"
	"fmt"

	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/store"
)

// InventoryTargets lists the enabled nodes a project's scope allows, sorted by
// name, as the plain maps `targets` and the selector read. A map never carries
// the credential, the host key or its policy. A run freezes this list when it
// starts (#117).
type InventoryTargets struct {
	Store store.NodeStore
	Scope inventory.Scope
}

// Targets implements api.TargetSource. It pages through every enabled node in
// scope, so the selector sees the whole inventory, and stops at limit picks.
// A node that moved between pages (a rename during the scan) is kept once.
func (t *InventoryTargets) Targets(ctx context.Context, project string, pick func([]map[string]any) []map[string]any, limit int) ([]map[string]any, error) {
	const pageSize = 200
	enabled := true
	var out []map[string]any
	seen := map[string]bool{}
	after := ""
	for len(out) < limit {
		page, err := t.Store.ListNodes(ctx, store.NodeFilter{Enabled: &enabled, AfterName: after, Limit: pageSize})
		if err != nil {
			return nil, fmt.Errorf("listing nodes: %w", err)
		}
		var batch []map[string]any
		for _, n := range page {
			if seen[n.ID] {
				continue
			}
			ok, err := t.Scope.Allows(ctx, project, n)
			if err != nil {
				return nil, fmt.Errorf("checking the node scope: %w", err)
			}
			if ok {
				seen[n.ID] = true
				batch = append(batch, targetOf(n))
			}
		}
		if pick != nil {
			batch = pick(batch)
		}
		out = append(out, batch...)
		if len(page) < pageSize {
			break
		}
		after = page[len(page)-1].Name
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func targetOf(n store.Node) map[string]any { return inventory.TargetMap(n) }
