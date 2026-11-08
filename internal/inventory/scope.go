package inventory

import (
	"context"

	"github.com/denyszorinets/damson/internal/store"
)

// Scope decides which nodes a project may use (ARCHITECTURE 12.1 node_scope).
type Scope interface {
	Allows(ctx context.Context, project string, n store.Node) (bool, error)
}

// AllNodes matches every node. It is the scope of tests and of the dev-nodes
// seeder; the controller uses ProjectScope.
type AllNodes struct{}

// Allows implements Scope.
func (AllNodes) Allows(context.Context, string, store.Node) (bool, error) { return true, nil }
