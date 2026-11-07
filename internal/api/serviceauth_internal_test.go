package api

import "testing"

func TestCovers(t *testing.T) {
	tests := []struct {
		scope, project string
		want           bool
	}{
		{"", "default", true},
		{"", "a/b", true},
		{"default", "default", true},
		{"a", "a/b", true},
		{"a", "a/b/c", true},
		{"a/b", "a", false},
		{"a", "ab", false}, // a sibling with a common prefix is not a descendant
		{"default", "other", false},
	}
	for _, tt := range tests {
		if got := covers(tt.scope, tt.project); got != tt.want {
			t.Errorf("covers(%q, %q) = %v, want %v", tt.scope, tt.project, got, tt.want)
		}
	}
}
