package expr

import (
	"fmt"

	"github.com/google/cel-go/cel"
)

// Variable names of a webhook request mapping.
const (
	VarBody    = "body"
	VarHeaders = "headers"
	VarQuery   = "query"
)

// Request is what a webhook options mapping sees: plain data of one HTTP
// request. Headers are lower-cased names to their first value; credentials
// headers are the caller's to leave out.
type Request struct {
	Body    any
	Headers map[string]any
	Query   map[string]any
}

// NewRequestEnv builds an Env for webhook options mappings: the variables are
// body, headers and query, and nothing else. Use CompileMapping and
// EvalMapping on it.
func NewRequestEnv(limits Limits) (*Env, error) {
	limits = limits.withDefaults()
	opts := baseOptions(limits)
	for _, v := range []string{VarBody, VarHeaders, VarQuery} {
		opts = append(opts, cel.Variable(v, cel.DynType))
	}
	e, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("building CEL request environment: %w", err)
	}
	return &Env{cel: e, limits: limits}, nil
}

// EvalMapping runs a program compiled by an Env from NewRequestEnv and returns
// the map it yields. A result that is not a map is an error. Errors wrap
// ErrEval and describe the failure, never request content.
func (e *Env) EvalMapping(p *Program, r Request) (map[string]any, error) {
	v, err := e.eval(p, map[string]any{
		VarBody: normalize(r.Body), VarHeaders: normalize(orEmpty(r.Headers)), VarQuery: normalize(orEmpty(r.Query)),
	})
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: the mapping must produce a map, not %s", ErrEval, jsonTypeName(v))
	}
	return m, nil
}
