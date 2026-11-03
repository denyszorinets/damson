package expr

import (
	"reflect"
	"strings"
	"testing"
)

func TestEvalList(t *testing.T) {
	env := newEnv(t, Limits{})
	tests := []struct {
		name, src string
		vars      Vars
		want      []any
		wantErr   string
	}{
		{"literal list", `["a", "b"]`, Vars{}, []any{"a", "b"}, ""},
		{"targets", `targets`, Vars{Targets: []any{map[string]any{"name": "web-01"}}}, []any{map[string]any{"name": "web-01"}}, ""},
		{"filter", `inputs.hosts.filter(h, h != "b")`, Vars{Inputs: map[string]any{"hosts": []any{"a", "b", "c"}}}, []any{"a", "c"}, ""},
		{"empty", `[]`, Vars{}, []any{}, ""},
		{"an object is not a list", `inputs`, Vars{Inputs: map[string]any{"a": 1.0}}, nil, "want a list"},
		{"a string is not a list", `"web"`, Vars{}, nil, "a string, want a list"},
		{"syntax error", `[1,`, Vars{}, nil, "Syntax error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := env.EvalList(tt.src, tt.vars)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v; want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v; want %#v", got, tt.want)
			}
		})
	}
}

func TestItemVariable(t *testing.T) {
	env := newEnv(t, Limits{})
	for _, tt := range []struct {
		src  string
		item any
		want any
	}{
		{`item`, "web-01", "web-01"},
		{`item.name`, map[string]any{"name": "web-01"}, "web-01"},
		{`item + 1`, 2.0, 3.0},
		{`item == "x"`, nil, false}, // outside a lane item is an empty object
	} {
		p, err := env.Compile(tt.src)
		if err != nil {
			t.Fatalf("%s: %v", tt.src, err)
		}
		got, err := env.Eval(p, Vars{Item: tt.item})
		if err != nil {
			t.Fatalf("%s: %v", tt.src, err)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %#v; want %#v", tt.src, got, tt.want)
		}
	}
}
