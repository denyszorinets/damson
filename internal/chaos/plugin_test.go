//go:build chaos

package chaos

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// gatePlugin logs a few lines and, for the word "one", blocks until the test
// opens the gate. It counts Execute calls per word and records whether the
// context was cancelled when it returned, so a test can prove a step ran
// exactly once and was never cancelled by a reconnect. State lives on the
// instance, not in package variables.
type gatePlugin struct {
	gate chan struct{}

	mu        sync.Mutex
	calls     map[string]int
	cancelled map[string]bool
}

func newGatePlugin() *gatePlugin {
	return &gatePlugin{gate: make(chan struct{}), calls: map[string]int{}, cancelled: map[string]bool{}}
}

func (g *gatePlugin) open() { close(g.gate) }

func (g *gatePlugin) Manifest() pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: "gate", Version: "1.0.0", Title: "gate", TargetMode: pluginsdk.TargetNone,
		InputSchema: pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
	}
}

func (g *gatePlugin) Validate(context.Context, json.RawMessage) error { return nil }

func (g *gatePlugin) Execute(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	var in struct{ Word string }
	_ = json.Unmarshal(req.Input, &in)
	g.mu.Lock()
	g.calls[in.Word]++
	g.mu.Unlock()
	for i := 1; i <= 3; i++ {
		emit.Log(pluginsdk.Stdout, in.Word+" line")
	}
	if in.Word == "one" {
		select {
		case <-g.gate:
		case <-ctx.Done():
		}
	}
	if in.Word == "hold" { // runs until cancelled, then fails the way a cancelled plugin does
		<-ctx.Done()
		g.mu.Lock()
		g.cancelled[in.Word] = true
		g.mu.Unlock()
		return pluginsdk.Result{Error: &pluginsdk.StepError{Message: "stopped"}}, nil
	}
	g.mu.Lock()
	g.cancelled[in.Word] = ctx.Err() != nil
	g.mu.Unlock()
	return pluginsdk.Result{Outputs: json.RawMessage(`{}`)}, nil
}

func (g *gatePlugin) callCount(word string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[word]
}

func (g *gatePlugin) wasCancelled(word string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cancelled[word]
}

func (g *gatePlugin) returned(word string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.cancelled[word]
	return ok
}
