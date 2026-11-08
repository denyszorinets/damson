package expr

import (
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
)

// Variable names of an access rule.
const (
	VarClaims = "claims"
	VarUser   = "user"
)

// Identity is what an access rule sees about a logged-in user: the raw claims
// of the id token and the few fields every rule may rely on.
type Identity struct {
	Sub    string
	Email  string
	Name   string
	Groups []string
	Claims map[string]any
}

// NewAccessEnv builds an Env for access rules (ARCHITECTURE 12.4): the variables
// are claims (the raw claims) and user (sub, email, name, groups), and nothing
// else. Use CompileRule and EvalRule on it.
func NewAccessEnv(limits Limits) (*Env, error) {
	limits = limits.withDefaults()
	opts := baseOptions(limits)
	for _, v := range []string{VarClaims, VarUser} {
		opts = append(opts, cel.Variable(v, cel.DynType))
	}
	e, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("building CEL access environment: %w", err)
	}
	return &Env{cel: e, limits: limits}, nil
}

// CompileRule compiles a rule: a boolean expression, or one whose type is only
// known at run time. A rule that can only produce another type is rejected.
func (e *Env) CompileRule(src string) (*Program, error) {
	if len(src) > e.limits.MaxExprBytes {
		return nil, fmt.Errorf("%w: longer than %d bytes", ErrCompile, e.limits.MaxExprBytes)
	}
	a, iss := e.cel.Compile(src)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("%w: %s", ErrCompile, iss.Err())
	}
	if t := a.OutputType(); !t.IsExactType(types.BoolType) && !t.IsExactType(types.DynType) {
		return nil, fmt.Errorf("%w: a rule must produce a boolean, not %s", ErrCompile, t)
	}
	prog, err := e.cel.Program(a,
		cel.EvalOptions(cel.OptTrackCost),
		cel.CostLimit(e.limits.CostLimit),
		cel.InterruptCheckFrequency(interruptChecks))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrCompile, err)
	}
	return &Program{prog: prog}, nil
}

// EvalRule runs a program compiled by CompileRule. A result that is not a
// boolean is an error. Errors wrap ErrEval and never quote claim values.
func (e *Env) EvalRule(p *Program, id Identity) (bool, error) {
	groups := make([]any, len(id.Groups))
	for i, g := range id.Groups {
		groups[i] = g
	}
	v, err := e.eval(p, map[string]any{
		VarClaims: normalize(orEmpty(id.Claims)),
		VarUser:   map[string]any{"sub": id.Sub, "email": id.Email, "name": id.Name, "groups": groups},
	})
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%w: a rule must produce a boolean, not %s", ErrEval, jsonTypeName(v))
	}
	return b, nil
}
