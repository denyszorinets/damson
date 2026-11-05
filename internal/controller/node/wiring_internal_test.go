package node

import (
	"log/slog"
	"testing"

	"github.com/denyszorinets/damson/internal/devnodes"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
)

// Where the dispatcher reads nodes and secrets from.
func TestDispatchSources(t *testing.T) {
	log := slog.New(slog.DiscardHandler)

	// No dev nodes, no KEK: inventory nodes, no secret source, no authorization.
	n := &Node{}
	nodes, sec, project := n.dispatchSources(Config{}, log)
	if _, ok := nodes.(*InventoryNodes); !ok || sec != nil || project != nil {
		t.Fatalf("no KEK: nodes %T, secrets %v, project set %v", nodes, sec, project != nil)
	}

	// With a KEK: the db secret source and project authorization too.
	n = &Node{secrets: &secretsdb.Backend{}}
	nodes, sec, project = n.dispatchSources(Config{}, log)
	if _, ok := nodes.(*InventoryNodes); !ok || sec == nil || project == nil {
		t.Fatalf("with KEK: nodes %T, secrets %v, project set %v", nodes, sec, project != nil)
	}
	if _, ok := sec.(*secretSource); !ok {
		t.Fatalf("secrets = %T", sec)
	}

	// The --dev-nodes file changes nothing here: it is seeded into the same
	// stores (ADR-0044), so there is no unauthorized path.
	f := &devnodes.File{}
	nodes, sec, project = n.dispatchSources(Config{DevFile: f}, log)
	if _, ok := nodes.(*InventoryNodes); !ok || sec == nil || project == nil {
		t.Fatalf("dev file: nodes %T, secrets %T, project set %v", nodes, sec, project != nil)
	}
}
