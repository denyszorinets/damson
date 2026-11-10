package expr

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func newEnv(t *testing.T, l Limits) *Env {
	t.Helper()
	e, err := NewEnv(l)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func testVars() Vars {
	return Vars{
		Inputs: map[string]any{"version": "1.2.3", "count": float64(3), "tags": []any{"a", "b"}},
		Run:    map[string]any{"id": "r1", "status": "running"},
		Job:    map[string]any{"name": "deploy"},
		Steps: map[string]any{
			"fetch": map[string]any{"status": "succeeded", "outputs": map[string]any{"path": "/tmp/x", "size": float64(10)}},
		},
		Status: Status{Success: true},
	}
}

func TestInterpolate(t *testing.T) {
	e := newEnv(t, Limits{})
	tests := []struct {
		name string
		in   string
		want any
	}{
		{"plain string", "hello", "hello"},
		{"exact keeps string", "${{ inputs.version }}", "1.2.3"},
		{"exact keeps number", "${{ inputs.count }}", float64(3)},
		{"exact keeps arithmetic", "${{ inputs.count + 1 }}", float64(4)},
		{"exact keeps bool", "${{ inputs.count > 2 }}", true},
		{"exact keeps list", "${{ inputs.tags }}", []any{"a", "b"}},
		{"exact keeps map", "${{ steps.fetch.outputs }}", map[string]any{"path": "/tmp/x", "size": float64(10)}},
		{"exact tolerates padding spaces inside", "${{inputs.count}}", float64(3)},
		{"concat stringifies number", "n=${{ inputs.count }}", "n=3"},
		{"concat stringifies list", "${{ inputs.tags }}!", `["a","b"]!`},
		{"concat two exprs", "${{ inputs.version }}-${{ job.name }}", "1.2.3-deploy"},
		{"leading space is concat", " ${{ inputs.count }}", " 3"},
		{"step output", "cat ${{ steps.fetch.outputs.path }}", "cat /tmp/x"},
		{"brace inside quotes", "${{ '}}' + inputs.version }}", "}}1.2.3"},
		{"escaped open", "$${{ inputs.version }}", "${{ inputs.version }}"},
		{"strings ext", "${{ inputs.version.upperAscii() }}", "1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.Interpolate(tt.in, testVars())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestInterpolateErrors(t *testing.T) {
	e := newEnv(t, Limits{})
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"unterminated", "${{ inputs.version", ErrCompile},
		{"empty", "${{  }}", ErrCompile},
		{"syntax", "${{ 1 + }}", ErrCompile},
		{"missing key", "${{ inputs.nope }}", ErrEval},
		{"missing step", "${{ steps.zzz.outputs.x }}", ErrEval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.Interpolate(tt.in, testVars())
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCostLimit(t *testing.T) {
	e := newEnv(t, Limits{CostLimit: 50})
	vars := testVars()
	vars.Inputs["big"] = make([]any, 1000)
	for i := range vars.Inputs["big"].([]any) {
		vars.Inputs["big"].([]any)[i] = float64(i)
	}
	_, err := e.Interpolate("${{ inputs.big.map(x, x * 2).size() }}", vars)
	if !errors.Is(err, ErrCost) {
		t.Fatalf("err = %v, want ErrCost", err)
	}
	if _, err := e.Interpolate("${{ 1 + 1 }}", vars); err != nil {
		t.Fatalf("cheap expression failed: %v", err)
	}
}

func TestResultSizeLimit(t *testing.T) {
	e := newEnv(t, Limits{MaxResultBytes: 64})
	vars := testVars()
	vars.Inputs["long"] = strings.Repeat("x", 100)
	for _, in := range []string{"${{ inputs.long }}", "pre ${{ inputs.long }}"} {
		if _, err := e.Interpolate(in, vars); !errors.Is(err, ErrResultSize) {
			t.Fatalf("%q: err = %v, want ErrResultSize", in, err)
		}
	}
}

func TestEvalBool(t *testing.T) {
	e := newEnv(t, Limits{})
	ok := Status{Success: true}
	failed := Status{Failure: true}
	cancelled := Status{Cancelled: true}
	tests := []struct {
		name string
		src  string
		st   Status
		want bool
	}{
		{"success()", "success()", ok, true},
		{"success() after failure", "success()", failed, false},
		{"failure() after failure", "failure()", failed, true},
		{"failure() when fine", "failure()", ok, false},
		{"always() after failure", "always()", failed, true},
		{"always() after cancel", "always()", cancelled, true},
		{"cancelled()", "cancelled()", cancelled, true},
		{"cancelled() not cancelled", "cancelled()", ok, false},
		{"implicit success: true expr", "inputs.count > 1", ok, true},
		{"implicit success: false expr", "inputs.count > 9", ok, false},
		{"implicit success blocks after failure", "inputs.count > 1", failed, false},
		{"explicit failure combined", "failure() && inputs.count > 1", failed, true},
		{"always combined false", "always() && inputs.count > 9", failed, false},
		{"step status", "steps.fetch.status == 'succeeded'", ok, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := testVars()
			vars.Status = tt.st
			got, err := e.EvalBool(tt.src, vars)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
	if _, err := e.EvalBool("inputs.count", testVars()); !errors.Is(err, ErrEval) {
		t.Fatalf("non-bool: err = %v", err)
	}
}

func TestResolveDoesNotMutateAndKeepsSecretRefs(t *testing.T) {
	e := newEnv(t, Limits{})
	in := map[string]any{
		"conn":    "secret://infra/s3",
		"command": "echo ${{ inputs.version }}",
		"list":    []any{"${{ inputs.count }}", float64(1), true, nil},
	}
	got, err := e.Resolve(in, testVars())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"conn":    "secret://infra/s3",
		"command": "echo 1.2.3",
		"list":    []any{float64(3), float64(1), true, nil},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	if in["command"] != "echo ${{ inputs.version }}" {
		t.Fatal("input mutated")
	}
}

// The context has no field for secret values: a secret:// ref is an ordinary
// string and is never dereferenced by expressions.
func TestSecretRefNotDereferenced(t *testing.T) {
	e := newEnv(t, Limits{})
	got, err := e.Interpolate("${{ 'secret://p/x' }}", testVars())
	if err != nil || got != "secret://p/x" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := e.Interpolate("${{ secrets.x }}", testVars()); !errors.Is(err, ErrCompile) {
		t.Fatalf("secrets must not be a context variable: %v", err)
	}
}

func TestDeterministic(t *testing.T) {
	e := newEnv(t, Limits{})
	a, _ := e.Interpolate("${{ inputs.tags }}-${{ steps.fetch.outputs }}", testVars())
	for range 20 {
		b, _ := e.Interpolate("${{ inputs.tags }}-${{ steps.fetch.outputs }}", testVars())
		if a != b {
			t.Fatalf("non-deterministic: %v vs %v", a, b)
		}
	}
}
