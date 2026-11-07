package expr

import (
	"errors"
	"strings"
	"testing"
)

func TestEvalMapping(t *testing.T) {
	env, err := NewRequestEnv(Limits{})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{
		Body:    map[string]any{"ref": "refs/heads/main", "n": float64(3)},
		Headers: map[string]any{"x-event": "push"},
		Query:   map[string]any{"env": "dev"},
	}
	tests := []struct {
		name, src string
		want      map[string]any
		wantErr   bool
	}{
		{"body, headers and query", `{"branch": body.ref.replace("refs/heads/", ""), "event": headers["x-event"], "env": query.env}`,
			map[string]any{"branch": "main", "event": "push", "env": "dev"}, false},
		{"numbers stay ints", `{"next": body.n + 1}`, map[string]any{"next": float64(4)}, false},
		{"not a map", `body.ref`, nil, true},
		{"missing key", `{"x": body.nope}`, nil, true},
		{"no job variables", `{"x": inputs.a}`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := env.Compile(tt.src)
			if err != nil {
				if !tt.wantErr {
					t.Fatalf("compile: %v", err)
				}
				return
			}
			got, err := env.EvalMapping(p, req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("%s = %v, want %v", k, got[k], v)
				}
			}
			if err != nil && !errors.Is(err, ErrEval) {
				t.Errorf("error %v does not wrap ErrEval", err)
			}
		})
	}
	// A request is not visible to ordinary job expressions.
	jobEnv, _ := NewEnv(Limits{})
	if _, err := jobEnv.Compile(`body.x`); err == nil || !strings.Contains(err.Error(), "undeclared") {
		t.Errorf("job env accepted `body`: %v", err)
	}
}
