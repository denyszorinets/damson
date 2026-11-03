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
	prog, err := e.env.Compile(job.Spec.Targets.Selector)
	if err != nil {
		return nil // validation compile-checks the selector; nothing matches a broken one
	}
	var out []map[string]any
	for _, n := range nodes {
		v, err := e.env.Eval(prog, expr.Vars{Inputs: inputs, Node: n})
		if err != nil {
			continue
		}
		if match, ok := v.(bool); ok && match {
			out = append(out, n)
		}
	}
	return out
}
