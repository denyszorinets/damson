package expr

import (
	"fmt"

	"github.com/google/cel-go/cel"
)

// NewNodeScopeEnv builds an Env for project node scopes (ARCHITECTURE 12.1): the
// only variable is node. Use CompileRule and EvalNodeScope on it.
func NewNodeScopeEnv(limits Limits) (*Env, error) {
	limits = limits.withDefaults()
	opts := append(baseOptions(limits), cel.Variable(VarNode, cel.DynType))
	e, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("building CEL node scope environment: %w", err)
	}
	return &Env{cel: e, limits: limits}, nil
}

// EvalNodeScope runs a program compiled by CompileRule on an Env from
// NewNodeScopeEnv against one node. A result that is not a boolean is an error.
func (e *Env) EvalNodeScope(p *Program, node map[string]any) (bool, error) {
	v, err := e.eval(p, map[string]any{VarNode: normalize(node)})
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%w: a node scope must produce a boolean, not %s", ErrEval, jsonTypeName(v))
	}
	return b, nil
}
