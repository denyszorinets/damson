package expr

import (
	"errors"
	"testing"
)

func TestNodeScope(t *testing.T) {
	env, err := NewNodeScopeEnv(Limits{})
	if err != nil {
		t.Fatal(err)
	}
	node := map[string]any{
		"name": "web-1", "address": "10.0.0.1", "os": "linux", "pool": "default", "transport": "ssh",
		"tags": []any{"prod", "web"}, "attributes": map[string]any{"env": "prod", "rack": float64(3)},
	}
	tests := []struct {
		rule string
		want bool
	}{
		{`'prod' in node.tags`, true},
		{`'dev' in node.tags`, false},
		{`node.attributes.env == 'prod' && node.os == 'linux'`, true},
		{`node.name.startsWith('db-')`, false},
		{`node.attributes.rack < 5`, true},
	}
	for _, tt := range tests {
		p, err := env.CompileRule(tt.rule)
		if err != nil {
			t.Fatalf("%s: %v", tt.rule, err)
		}
		got, err := env.EvalNodeScope(p, node)
		if err != nil || got != tt.want {
			t.Errorf("%s = %v, %v; want %v", tt.rule, got, err, tt.want)
		}
	}
	// A missing attribute is an error, which the scope turns into "not allowed".
	p, _ := env.CompileRule(`node.attributes.region == 'eu'`)
	if _, err := env.EvalNodeScope(p, node); !errors.Is(err, ErrEval) {
		t.Errorf("missing attribute: %v; want ErrEval", err)
	}
	// Only `node` exists.
	if _, err := env.CompileRule(`claims.groups.size() > 0`); !errors.Is(err, ErrCompile) {
		t.Errorf("claims in a node scope: %v; want ErrCompile", err)
	}
	if _, err := env.CompileRule(`node.name`); err != nil { // runtime-typed: compiles
		t.Errorf("dyn rule: %v", err)
	}
}
