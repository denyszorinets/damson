package expr

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/ext"
)

// Default limits. They are generous for real specs and tight enough to stop
// runaway expressions.
const (
	DefaultCostLimit      uint64 = 100_000
	DefaultMaxResultBytes        = 64 << 10
	DefaultMaxExprBytes          = 4 << 10
)

// Limits bounds expression evaluation. Zero fields take the defaults.
type Limits struct {
	// CostLimit is the CEL runtime cost budget per expression.
	CostLimit uint64
	// MaxResultBytes caps the JSON size of one expression result.
	MaxResultBytes int
	// MaxExprBytes caps the source length of one expression.
	MaxExprBytes int
}

func (l Limits) withDefaults() Limits {
	if l.CostLimit == 0 {
		l.CostLimit = DefaultCostLimit
	}
	if l.MaxResultBytes == 0 {
		l.MaxResultBytes = DefaultMaxResultBytes
	}
	if l.MaxExprBytes == 0 {
		l.MaxExprBytes = DefaultMaxExprBytes
	}
	return l
}

// Variable names of the evaluation context. All are dynamically typed.
// `node`, `targets` and `error` are declared now so that specs using them
// compile; the engine binds them from M3 (for_each and catch).
const (
	VarInputs  = "inputs"
	VarRun     = "run"
	VarJob     = "job"
	VarSteps   = "steps"
	VarNode    = "node"
	VarTargets = "targets"
	VarError   = "error"
)

// Status functions are macros that expand to these hidden variables, so the
// environment itself stays free of per-run state.
const (
	statusPrefix = "_status_"
	varSuccess   = statusPrefix + "success"
	varFailure   = statusPrefix + "failure"
	varAlways    = statusPrefix + "always"
	varCancelled = statusPrefix + "cancelled"

	maxRecursion    = 64
	interruptChecks = 100
)

// Sentinel errors, to be matched with errors.Is.
var (
	ErrCost       = errors.New("expression cost limit exceeded")
	ErrResultSize = errors.New("expression result too large")
	ErrCompile    = errors.New("invalid expression")
	ErrEval       = errors.New("expression evaluation failed")
)

// Env compiles and evaluates CEL expressions. It is safe for concurrent use.
type Env struct {
	cel    *cel.Env
	limits Limits
}

// NewEnv builds an Env with the given limits.
func NewEnv(limits Limits) (*Env, error) {
	limits = limits.withDefaults()
	opts := []cel.EnvOption{
		ext.Strings(),
		cel.ParserRecursionLimit(maxRecursion),
		cel.ParserExpressionSizeLimit(limits.MaxExprBytes),
		cel.Macros(
			statusMacro("success", varSuccess), statusMacro("failure", varFailure),
			statusMacro("always", varAlways), statusMacro("cancelled", varCancelled)),
	}
	for _, v := range []string{VarInputs, VarRun, VarJob, VarSteps, VarNode, VarTargets, VarError} {
		opts = append(opts, cel.Variable(v, cel.DynType))
	}
	for _, v := range []string{varSuccess, varFailure, varAlways, varCancelled} {
		opts = append(opts, cel.Variable(v, cel.BoolType))
	}
	e, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("building CEL environment: %w", err)
	}
	return &Env{cel: e, limits: limits}, nil
}

func statusMacro(fn, ident string) cel.Macro {
	return cel.GlobalMacro(fn, 0,
		func(eh cel.MacroExprFactory, _ ast.Expr, _ []ast.Expr) (ast.Expr, *common.Error) {
			return eh.NewIdent(ident), nil
		})
}

// Program is a compiled expression.
type Program struct {
	prog       cel.Program
	usesStatus bool
}

// UsesStatus reports whether the expression calls success(), failure(),
// always() or cancelled().
func (p *Program) UsesStatus() bool { return p.usesStatus }

// Compile parses and type-checks src. Errors wrap ErrCompile.
func (e *Env) Compile(src string) (*Program, error) {
	if len(src) > e.limits.MaxExprBytes {
		return nil, fmt.Errorf("%w: longer than %d bytes", ErrCompile, e.limits.MaxExprBytes)
	}
	a, iss := e.cel.Compile(src)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("%w: %s", ErrCompile, iss.Err())
	}
	prog, err := e.cel.Program(a,
		cel.EvalOptions(cel.OptTrackCost),
		cel.CostLimit(e.limits.CostLimit),
		cel.InterruptCheckFrequency(interruptChecks))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrCompile, err)
	}
	return &Program{prog: prog, usesStatus: usesStatus(a)}, nil
}

func usesStatus(a *cel.Ast) bool {
	for _, r := range a.NativeRep().ReferenceMap() {
		if strings.HasPrefix(r.Name, statusPrefix) {
			return true
		}
	}
	return false
}
