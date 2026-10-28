package expr

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"google.golang.org/protobuf/types/known/structpb"
)

// Status carries the values of the status functions (§5.3 rule 8).
// always() is constant true and has no field.
type Status struct {
	Success   bool
	Failure   bool
	Cancelled bool
}

// Vars is the whole evaluation context. It contains plain data only; there is
// deliberately no way to put a secret value in it.
type Vars struct {
	Inputs  map[string]any
	Run     map[string]any
	Job     map[string]any
	Steps   map[string]any
	Node    map[string]any
	Targets []any
	Error   map[string]any
	Status  Status
}

func (v Vars) activation() map[string]any {
	return map[string]any{
		VarInputs:  normalize(orEmpty(v.Inputs)),
		VarRun:     normalize(orEmpty(v.Run)),
		VarJob:     normalize(orEmpty(v.Job)),
		VarSteps:   normalize(orEmpty(v.Steps)),
		VarNode:    normalize(orEmpty(v.Node)),
		VarTargets: normalize(orEmptyList(v.Targets)),
		VarError:   normalize(orEmpty(v.Error)),

		varSuccess:   v.Status.Success,
		varFailure:   v.Status.Failure,
		varAlways:    true,
		varCancelled: v.Status.Cancelled,
	}
}

// normalize deep-copies JSON-decoded data, turning whole float64 numbers into
// int64 so that `inputs.count + 1` type-checks at run time: encoding/json
// yields float64 for every number, while CEL does not mix double and int.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = normalize(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalize(x)
		}
		return out
	case float64:
		if t == math.Trunc(t) && math.Abs(t) <= 1<<53 {
			return int64(t)
		}
		return t
	default:
		return v
	}
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orEmptyList(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}

// Eval runs the program and returns the result as a JSON-compatible Go value
// (nil, bool, float64, string, []any, map[string]any).
func (e *Env) Eval(p *Program, vars Vars) (any, error) {
	out, _, err := p.prog.Eval(vars.activation())
	if err != nil {
		if strings.Contains(err.Error(), "actual cost limit exceeded") {
			return nil, fmt.Errorf("%w: %w (limit %d)", ErrEval, ErrCost, e.limits.CostLimit)
		}
		return nil, fmt.Errorf("%w: %s", ErrEval, err)
	}
	v, err := toJSONValue(out)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding result: %s", ErrEval, err)
	}
	if len(b) > e.limits.MaxResultBytes {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrResultSize, len(b), e.limits.MaxResultBytes)
	}
	return v, nil
}

// EvalBool evaluates a condition (a step's `when`). A non-bool result is an
// error. When the expression calls no status function, success() is implied:
// the condition is "success() && (src)".
func (e *Env) EvalBool(src string, vars Vars) (bool, error) {
	p, err := e.Compile(src)
	if err != nil {
		return false, err
	}
	v, err := e.Eval(p, vars)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%w: condition returned %T, want bool", ErrEval, v)
	}
	if !p.UsesStatus() && !vars.Status.Success {
		return false, nil
	}
	return b, nil
}

func toJSONValue(v ref.Val) (any, error) {
	if types.IsError(v) {
		return nil, fmt.Errorf("%w: %v", ErrEval, v)
	}
	native, err := v.ConvertToNative(reflect.TypeFor[*structpb.Value]())
	if err != nil {
		return nil, fmt.Errorf("%w: result is not representable as JSON: %s", ErrEval, err)
	}
	pv, ok := native.(*structpb.Value)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected result type %T", ErrEval, native)
	}
	return pv.AsInterface(), nil
}
