package engine

import "github.com/denyszorinets/damson/internal/expr"

// SelectTargets filters the nodes the controller knows by spec.targets.selector
// and returns the ones that match, in their given order: the value of `targets`
// for a run. Without a selector every node matches. The selector is a CEL
// expression over `node` (name, address, tags, attributes, pool, os, transport)
// and `inputs`; a node for which it does not evaluate to true, including one
// whose evaluation fails, does not match (the same precedent as retry_on).
//
// It is pure: the nodes come from the caller, and the order is theirs.
func (e *Engine) SelectTargets(job *Job, inputs map[string]any, nodes []map[string]any) []map[string]any {
	if len(nodes) == 0 {
		return nil
	}
	if job == nil || job.Spec.Targets == nil || job.Spec.Targets.Selector == "" {
		return nodes
	}
	m, err := e.CompileSelector(job.Spec.Targets.Selector)
	if err != nil {
		return nil // validation compile-checks the selector; nothing matches a broken one
	}
	var out []map[string]any
	for _, n := range nodes {
		if m(inputs, n) {
			out = append(out, n)
		}
	}
	return out
}

// TargetMatcher reports whether a node (the map `node` evaluates to) matches
// a compiled selector; an evaluation error does not match.
type TargetMatcher func(inputs, node map[string]any) bool

// CompileSelector compiles a selector once for many nodes. Pure.
func (e *Engine) CompileSelector(selector string) (TargetMatcher, error) {
	prog, err := e.env.Compile(selector)
	if err != nil {
		return nil, err
	}
	return func(inputs, node map[string]any) bool {
		v, err := e.env.Eval(prog, expr.Vars{Inputs: inputs, Node: node})
		if err != nil {
			return false
		}
		match, ok := v.(bool)
		return ok && match
	}, nil
}
