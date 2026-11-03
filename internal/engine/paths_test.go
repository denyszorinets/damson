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

func TestLanePaths(t *testing.T) {
	job := &Job{Spec: Spec{Steps: []Step{
		{ID: "per-node", Type: StepTypeGroup, ForEach: "targets",
			Steps: []Step{{ID: "install", Plugin: "a"}}, Catch: []Step{{ID: "rollback", Plugin: "b"}}, Finally: []Step{{ID: "tidy", Plugin: "c"}}},
		{ID: "plain", Type: StepTypeGroup, Steps: []Step{{ID: "x", Plugin: "d"}}},
	}}}
	tests := []struct {
		path   string
		plugin string
		ok     bool
	}{
		{"per-node", "", true},
		{"per-node/for_each[web-01]", "", true}, // the lane row is the for_each group's own spec
		{"per-node/for_each[web-01]/install", "a", true},
		{"per-node/for_each[web-01]/catch/rollback", "b", true},
		{"per-node/for_each[web-01]/finally/tidy", "c", true},
		{"per-node/for_each[0]/install", "a", true},
		{"per-node/install", "", false},            // a direct child of a for_each group
		{"per-node/for_each[]/install", "", false}, // an empty key
		{"per-node/for_each[web-01]", "", true},
		{"per-node/for_each[web-01]/catch", "", false},
		{"plain/for_each[web-01]/x", "", false}, // lanes only under a for_each group
		{"plain/x", "d", true},
	}
	for _, tc := range tests {
		s, ok := job.StepAt(tc.path)
		if ok != tc.ok || (ok && tc.plugin != "" && s.Plugin != tc.plugin) {
			t.Errorf("StepAt(%q) = plugin %q, %v; want %q, %v", tc.path, s.Plugin, ok, tc.plugin, tc.ok)
		}
	}
	if s, _ := job.StepAt("per-node/for_each[web-01]"); s.ForEach != "targets" {
		t.Error("the lane row must resolve to the for_each group's spec")
	}
}

func TestStepIDOfAndLaneOf(t *testing.T) {
	for path, want := range map[string]string{
		"a": "a", "g/x": "x", "g/for_each[web-01]": "g", "g/for_each[web-01]/install": "install", "g/for_each[web-01]/catch/r": "r",
	} {
		if got := StepIDOf(path); got != want {
			t.Errorf("StepIDOf(%q) = %q; want %q", path, got, want)
		}
	}
	group, key, ok := laneOf("deploy/per-node/for_each[web.01]/install")
	if !ok || group != "deploy/per-node" || key != "web.01" {
		t.Errorf("laneOf = %q, %q, %v", group, key, ok)
	}
	if _, _, ok := laneOf("deploy/per-node/install"); ok {
		t.Error("laneOf found a lane in a path without one")
	}
	if LanePath("g", "web-01") != "g/for_each[web-01]" {
		t.Errorf("LanePath = %q", LanePath("g", "web-01"))
	}
}

func TestFinallyOwnersWithLanes(t *testing.T) {
	got := finallyOwners("g/for_each[a]/finally/x")
	if len(got) != 1 || got[0] != "g/for_each[a]" {
		t.Fatalf("owners = %v; want the lane row", got)
	}
	if InFinally("g/for_each[finally]/x") {
		t.Error("a lane key is not a finally marker")
	}
}
