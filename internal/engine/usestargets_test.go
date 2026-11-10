package engine

import "testing"

func TestUsesTargets(t *testing.T) {
	steps := func(s ...Step) *Job { return &Job{Spec: Spec{Steps: s}} }
	cases := []struct {
		name string
		job  *Job
		want bool
	}{
		{"nil", nil, false},
		{"plain step", steps(Step{ID: "a", Plugin: "ssh", With: map[string]any{"node": "web"}}), false},
		{"selector", &Job{Spec: Spec{Targets: &Targets{Selector: "true"}}}, true},
		{"for_each targets", steps(Step{ID: "g", Type: "group", ForEach: "targets"}), true},
		{"nested for_each", steps(Step{ID: "g", Type: "group", Steps: []Step{{ID: "h", Type: "group", ForEach: "targets"}}}), true},
		{"in a condition", steps(Step{ID: "a", Plugin: "ssh", When: "size(targets) > 0"}), true},
		{"in an interpolation", steps(Step{ID: "a", Plugin: "ssh", With: map[string]any{"c": "echo ${{ size(targets) }}"}}), true},
	}
	for _, c := range cases {
		if got := UsesTargets(c.job); got != c.want {
			t.Errorf("%s: UsesTargets = %v, want %v", c.name, got, c.want)
		}
	}
}
