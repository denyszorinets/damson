package inventory

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

type fakeChains struct {
	projects map[string]string // path to node scope
	calls    int
}

func (f *fakeChains) ListProjectChain(_ context.Context, path string) ([]store.Project, error) {
	f.calls++
	var out []store.Project
	for p := path; p != ""; p = store.ParentPath(p) {
		if s, ok := f.projects[p]; ok {
			out = append([]store.Project{{Path: p, NodeScope: s}}, out...)
		}
	}
	return out, nil
}

func TestProjectScope(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	chains := &fakeChains{projects: map[string]string{
		"all": "", "prod": `'prod' in node.tags`, "prod/eu": `node.attributes.region == 'eu'`,
		"broken": `((`, "typed": `node.name`, "noattr": `node.attributes.nope == 'x'`,
	}}
	sc, err := NewProjectScope(chains, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	eu := store.Node{Name: "a", Tags: []string{"prod"}, Attributes: map[string]any{"region": "eu"}}
	us := store.Node{Name: "b", Tags: []string{"prod"}, Attributes: map[string]any{"region": "us"}}
	dev := store.Node{Name: "c", Tags: []string{"dev"}, Attributes: map[string]any{"region": "eu"}}
	tests := []struct {
		project string
		node    store.Node
		want    bool
	}{
		{"all", dev, true},
		{"unknown", dev, true}, // no project row, no scope
		{"prod", eu, true},
		{"prod", dev, false},
		{"prod/eu", eu, true},  // own and parent scope both hold
		{"prod/eu", us, false}, // own scope fails
		{"prod/eu", dev, false},
		{"prod/us", dev, false}, // a child with no scope still has the parent's
		{"broken", eu, false},
		{"typed", eu, false},
		{"noattr", eu, false},
	}
	for _, tt := range tests {
		got, err := sc.Allows(context.Background(), tt.project, tt.node)
		if err != nil || got != tt.want {
			t.Errorf("%s/%s = %v, %v; want %v", tt.project, tt.node.Name, got, err, tt.want)
		}
	}

	// The chain is cached for a short time.
	calls := chains.calls
	_, _ = sc.Allows(context.Background(), "prod", eu)
	_, _ = sc.Allows(context.Background(), "prod", us)
	if chains.calls != calls {
		t.Errorf("chain read %d more times within the TTL", chains.calls-calls)
	}
	chains.projects["prod"] = `true`
	now = now.Add(scopeTTL + time.Second)
	if ok, _ := sc.Allows(context.Background(), "prod", dev); !ok {
		t.Error("an edited scope must apply after the TTL")
	}
}

// A scope that does not compile is compiled and logged once per cache fill, not
// once per node, and allows nothing.
func TestBrokenScopeIsLoggedOncePerFill(t *testing.T) {
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, nil))
	chains := &fakeChains{projects: map[string]string{"bad": `((`}}
	sc, _ := NewProjectScope(chains, nil, log)
	for i := 0; i < 50; i++ {
		if ok, _ := sc.Allows(context.Background(), "bad", store.Node{Name: "n"}); ok {
			t.Fatal("a broken scope allowed a node")
		}
	}
	if n := strings.Count(logs.String(), "does not compile"); n != 1 {
		t.Errorf("logged %d times for 50 nodes; want once", n)
	}
}

// Evaluation errors may quote node values, so the log must not.
func TestEvaluationErrorLogHoldsNoNodeData(t *testing.T) {
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	chains := &fakeChains{projects: map[string]string{"p": `int(node.attributes.x) > 1`}}
	sc, _ := NewProjectScope(chains, nil, log)
	n := store.Node{Name: "n", Attributes: map[string]any{"x": "sekrit-value-abc"}}
	if ok, _ := sc.Allows(context.Background(), "p", n); ok {
		t.Fatal("allowed")
	}
	if strings.Contains(logs.String(), "sekrit-value-abc") {
		t.Fatalf("node data in the log: %s", logs.String())
	}
}
