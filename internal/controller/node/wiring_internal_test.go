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

	// --dev-nodes wins and is not authorized (ADR-0034), whatever else is set.
	f := &devnodes.File{}
	nodes, sec, project = n.dispatchSources(Config{DevNodes: f}, log)
	if nodes != any(f) || sec != any(f) || project != nil {
		t.Fatalf("dev nodes: nodes %T, secrets %T, project set %v", nodes, sec, project != nil)
	}
}
