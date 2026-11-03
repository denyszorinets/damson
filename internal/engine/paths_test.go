package engine

import "testing"

func TestInFinally(t *testing.T) {
	for path, want := range map[string]bool{
		"a": false, "g/x": false, "g/catch/x": false, "g/finally/x": true, "g/finally/h/y": true, "g/h/finally/y": true,
	} {
		if got := InFinally(path); got != want {
			t.Errorf("InFinally(%q) = %v; want %v", path, got, want)
		}
	}
}

func TestStepAt(t *testing.T) {
	job := &Job{Spec: Spec{Steps: []Step{
		{ID: "top", Plugin: "ssh"},
		{ID: "g", Type: StepTypeGroup,
			Steps:   []Step{{ID: "body", Plugin: "a"}, {ID: "inner", Type: StepTypeGroup, Steps: []Step{{ID: "deep", Plugin: "b"}}}},
			Catch:   []Step{{ID: "rescue", Plugin: "c"}},
			Finally: []Step{{ID: "tidy", Plugin: "d"}}},
	}}}
	tests := []struct {
		path   string
		plugin string
		ok     bool
	}{
		{"top", "ssh", true},
		{"g/body", "a", true},
		{"g/inner/deep", "b", true},
		{"g/catch/rescue", "c", true},
		{"g/finally/tidy", "d", true},
		{"g", "", true},
		{"nope", "", false},
		{"top/x", "", false},    // a plugin step has no children
		{"g/catch", "", false},  // a marker with no id
		{"g/rescue", "", false}, // catch children need the marker
		{"g/body/x", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		s, ok := job.StepAt(tc.path)
		if ok != tc.ok || (ok && s.Plugin != tc.plugin) {
			t.Errorf("StepAt(%q) = plugin %q, %v; want %q, %v", tc.path, s.Plugin, ok, tc.plugin, tc.ok)
		}
	}
}
