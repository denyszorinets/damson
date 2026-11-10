package policy

import (
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/store"
)

func id(groups ...string) expr.Identity {
	gs := make([]any, len(groups))
	for i, g := range groups {
		gs[i] = g
	}
	return expr.Identity{Sub: "u", Email: "u@example.org", Groups: groups, Claims: map[string]any{"groups": gs}}
}

func rule(role, src string) store.AccessRule { return store.AccessRule{Role: role, Rule: src} }

func TestRole(t *testing.T) {
	e, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		id    expr.Identity
		lists [][]store.AccessRule
		want  string
	}{
		{"no rules", id("ops"), nil, ""},
		{"no match", id("ops"), [][]store.AccessRule{{rule("runner", `'billing' in claims.groups`)}}, ""},
		{"one match", id("ops"), [][]store.AccessRule{{rule("runner", `'ops' in claims.groups`)}}, "runner"},
		{"highest of several", id("ops", "leads"), [][]store.AccessRule{{
			rule("viewer", `true`), rule("editor", `'leads' in user.groups`), rule("runner", `'ops' in user.groups`)}}, "editor"},
		{"max across inherited lists", id("ops"), [][]store.AccessRule{
			{rule("viewer", `true`)}, {rule("admin", `'ops' in claims.groups`)}, {rule("runner", `true`)}}, "admin"},
		{"missing claim grants nothing, others still apply", id("ops"), [][]store.AccessRule{{
			rule("admin", `claims.nope == 'x'`), rule("viewer", `true`)}}, "viewer"},
		{"broken rule grants nothing", id("ops"), [][]store.AccessRule{{rule("admin", `((`), rule("viewer", `true`)}}, "viewer"},
		{"non-bool grants nothing", id("ops"), [][]store.AccessRule{{rule("admin", `claims.groups`), rule("viewer", `true`)}}, "viewer"},
		{"unknown role never wins", id("ops"), [][]store.AccessRule{{rule("root", `true`)}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := e.Role(tt.id, tt.lists...); got != tt.want {
				t.Errorf("Role = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCostLimitedRuleGrantsNothing(t *testing.T) {
	e, _ := New(nil)
	hostile := rule("admin", `claims.groups.all(a, claims.groups.all(b, claims.groups.all(c, claims.groups.all(d, claims.groups.all(f, a+b+c+d+f != '')))))`)
	big := make([]string, 60)
	for i := range big {
		big[i] = strings.Repeat("g", i+1)
	}
	if got := e.Role(id(big...), []store.AccessRule{hostile, rule("viewer", "true")}); got != "viewer" {
		t.Errorf("Role = %q; a rule over the cost limit must grant nothing", got)
	}
}

func TestValidate(t *testing.T) {
	e, _ := New(nil)
	errs := e.Validate([]store.AccessRule{rule("viewer", `true`), rule("viewer", `1 +`), rule("viewer", `42`)})
	if len(errs) != 2 || errs[0].Index != 1 || errs[1].Index != 2 {
		t.Fatalf("errs = %+v", errs)
	}
	if e.Check(`'a' in claims.groups`) != nil {
		t.Error("a valid rule must pass")
	}
}

func TestProgramsAreCached(t *testing.T) {
	e, _ := New(nil)
	a, _ := e.program(`true`)
	b, _ := e.program(`true`)
	if a != b {
		t.Error("the same source must compile once")
	}
}

func TestExplainNamesTheGrantingRule(t *testing.T) {
	e, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		id          expr.Identity
		lists       [][]store.AccessRule
		role        string
		list, index int
	}{
		{"none", id("ops"), [][]store.AccessRule{{rule("runner", `'x' in claims.groups`)}}, "", -1, -1},
		{"first list, second rule", id("ops"), [][]store.AccessRule{{rule("viewer", `false`), rule("runner", `'ops' in claims.groups`)}}, "runner", 0, 1},
		{"an ancestor's rule wins by role", id("ops"), [][]store.AccessRule{{rule("admin", `'ops' in claims.groups`)}, {rule("runner", `true`)}}, "admin", 0, 0},
		{"a later list raises the role", id("ops"), [][]store.AccessRule{{rule("viewer", `true`)}, {rule("editor", `'ops' in user.groups`)}}, "editor", 1, 0},
		{"equal roles: the first rule is named", id("ops"), [][]store.AccessRule{{rule("runner", `true`)}, {rule("runner", `true`)}}, "runner", 0, 0},
		{"a broken rule is skipped", id("ops"), [][]store.AccessRule{{rule("admin", `((`), rule("viewer", `true`)}}, "viewer", 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := e.Explain(tt.id, tt.lists...)
			if g.Role != tt.role || g.List != tt.list || g.Index != tt.index {
				t.Errorf("Explain = %+v, want role %q list %d index %d", g, tt.role, tt.list, tt.index)
			}
			if r := e.Role(tt.id, tt.lists...); r != g.Role {
				t.Errorf("Role = %q but Explain says %q: enforcement and explanation differ", r, g.Role)
			}
		})
	}
}
