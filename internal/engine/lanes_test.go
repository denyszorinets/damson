package engine

import (
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/expr"
)

func TestLaneKeyOf(t *testing.T) {
	tests := []struct {
		item any
		i    int
		want string
	}{
		{"web-01", 0, "web-01"},
		{map[string]any{"name": "db-1", "x": 1.0}, 4, "db-1"},
		{map[string]any{"name": 7.0}, 2, "2"}, // a name that is not a string falls back to the index
		{map[string]any{"host": "x"}, 3, "3"},
		{42.0, 5, "5"},
		{true, 6, "6"},
		{nil, 7, "7"},
		{[]any{"a"}, 8, "8"},
	}
	for _, tc := range tests {
		if got := laneKeyOf(tc.item, tc.i); got != tc.want {
			t.Errorf("laneKeyOf(%v, %d) = %q; want %q", tc.item, tc.i, got, tc.want)
		}
	}
}

func TestEvalLanes(t *testing.T) {
	e := newEngine(t, expr.Limits{})
	d := &decider{e: e, st: RunState{Inputs: map[string]any{
		"hosts": []any{"a", "b", "c"},
		"nodes": []any{map[string]any{"name": "web-01"}, map[string]any{"name": "web-02"}},
		"dup":   []any{"a", "a"},
		"bad":   []any{"has/slash"},
		"mixed": []any{"x", 3.0, map[string]any{"id": 1.0}},
		"empty": []any{},
	}}}
	sc := newScope("", nil, nil)
	tests := []struct {
		src     string
		wantKey []string
		wantErr string
	}{
		{"inputs.hosts", []string{"a", "b", "c"}, ""},
		{"inputs.nodes", []string{"web-01", "web-02"}, ""},
		{"inputs.mixed", []string{"x", "1", "2"}, ""},
		{"inputs.empty", []string{}, ""},
		{"inputs.hosts.filter(h, h != 'b')", []string{"a", "c"}, ""},
		{"inputs.dup", nil, "same lane key"},
		{"inputs.bad", nil, "lane key"},
		{"inputs.hosts[0]", nil, "want a list"},
		{"1 +", nil, "Syntax error"},
	}
	for _, tc := range tests {
		lanes, err := d.evalLanes(sc, Step{ForEach: tc.src}, false)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v; want %q", tc.src, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.src, err)
			continue
		}
		var keys []string
		for _, l := range lanes {
			keys = append(keys, l.Key)
		}
		if strings.Join(keys, ",") != strings.Join(tc.wantKey, ",") {
			t.Errorf("%s: keys = %v; want %v", tc.src, keys, tc.wantKey)
		}
	}
}

func TestEvalLanesLimit(t *testing.T) {
	e := newEngine(t, expr.Limits{})
	d := &decider{e: e, st: RunState{}}
	_, err := d.evalLanes(newScope("", nil, nil), Step{ForEach: "[1,2,3].map(x, x)"}, false)
	if err != nil {
		t.Fatal(err)
	}
	items := make([]any, maxLanes+1)
	for i := range items {
		items[i] = float64(i)
	}
	d.st.Inputs = map[string]any{"big": items}
	if _, err := d.evalLanes(newScope("", nil, nil), Step{ForEach: "inputs.big"}, false); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("err = %v; want the lane limit", err)
	}
}
