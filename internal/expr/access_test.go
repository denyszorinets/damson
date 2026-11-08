package expr

import (
	"errors"
	"testing"
)

func TestAccessRules(t *testing.T) {
	env, err := NewAccessEnv(Limits{})
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{
		Sub: "u1", Email: "a@example.org", Groups: []string{"damson-admins", "ops"},
		Claims: map[string]any{"groups": []any{"damson-admins", "ops"}, "email_verified": true, "hd": "example.org"},
	}
	tests := []struct {
		name, rule string
		want       bool
	}{
		{"group claim", `'damson-admins' in claims.groups`, true},
		{"user groups", `'billing' in user.groups`, false},
		{"claim equality", `claims.hd == 'example.org' && claims.email_verified`, true},
		{"missing claim is an error, not a match", `claims.nope == 'x'`, false},
		{"email", `user.email.endsWith('@example.org')`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := env.CompileRule(tt.rule)
			if err != nil {
				t.Fatal(err)
			}
			got, err := env.EvalRule(p, id)
			if tt.name == "missing claim is an error, not a match" {
				if !errors.Is(err, ErrEval) || got {
					t.Fatalf("got %v, %v; want ErrEval and false", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %v, %v; want %v", got, err, tt.want)
			}
		})
	}

	for _, bad := range []string{`1 + 1`, `"str"`, `claims.`, `unknown_var == 1`, `claims.groups.map(g, g + g).size() > 0 &&`} {
		if _, err := env.CompileRule(bad); !errors.Is(err, ErrCompile) {
			t.Errorf("rule %q: %v; want ErrCompile", bad, err)
		}
	}
	// Only known at run time: compiles, but a non-bool result is an error.
	p, err := env.CompileRule(`claims.hd`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.EvalRule(p, id); !errors.Is(err, ErrEval) {
		t.Errorf("string result: %v; want ErrEval", err)
	}
	// The cost limit stops a hostile rule.
	small, _ := NewAccessEnv(Limits{CostLimit: 50})
	p, err = small.CompileRule(`claims.groups.all(a, claims.groups.all(b, claims.groups.all(c, a + b + c != "")))`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := small.EvalRule(p, id); !errors.Is(err, ErrCost) {
		t.Errorf("costly rule: %v; want ErrCost", err)
	}
}
