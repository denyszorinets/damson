// Package policy decides the role a user holds from CEL access rules
// (ARCHITECTURE 12.4, ADR-0064). It is pure: it reads no store and no clock.
package policy

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/store"
)

// maxCached bounds the compiled-program cache; it is cleared when full.
const maxCached = 4096

// Evaluator compiles and evaluates access rules. Programs are cached by their
// source text, so a rule that did not change is compiled once.
type Evaluator struct {
	env *expr.Env
	log *slog.Logger

	mu    sync.Mutex
	cache map[string]*expr.Program
}

// New returns an Evaluator. A nil logger uses the default.
func New(log *slog.Logger) (*Evaluator, error) {
	env, err := expr.NewAccessEnv(expr.Limits{})
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Evaluator{env: env, log: log, cache: map[string]*expr.Program{}}, nil
}

// Check compiles a rule without running it. The error says what is wrong with
// the expression and never quotes it.
func (e *Evaluator) Check(rule string) error {
	_, err := e.program(rule)
	return err
}

func (e *Evaluator) program(rule string) (*expr.Program, error) {
	e.mu.Lock()
	p, ok := e.cache[rule]
	e.mu.Unlock()
	if ok {
		return p, nil
	}
	p, err := e.env.CompileRule(rule)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if len(e.cache) >= maxCached {
		clear(e.cache)
	}
	e.cache[rule] = p
	e.mu.Unlock()
	return p, nil
}

// Role is the highest role granted to id by any rule of any list: a rule grants
// its role when it evaluates to true. A rule that does not compile or does not
// evaluate (for example because a claim is missing) grants nothing; the log
// names the rule's role, never claim values (at debug level: a rule on a claim some users lack fails on every request). "" means no role.
func (e *Evaluator) Role(id expr.Identity, lists ...[]store.AccessRule) string {
	best := ""
	for _, rules := range lists {
		for i, r := range rules {
			if store.RoleRank(r.Role) <= store.RoleRank(best) {
				continue // cannot raise the result; skip the evaluation
			}
			p, err := e.program(r.Rule)
			if err != nil {
				e.log.Warn("access rule does not compile; it grants nothing", "role", r.Role, "index", i)
				continue
			}
			ok, err := e.env.EvalRule(p, id)
			if err != nil {
				e.log.Debug("access rule did not evaluate; it grants nothing", "role", r.Role, "index", i, "error", err)
				continue
			}
			if ok {
				best = r.Role
			}
		}
	}
	return best
}

// FieldError locates one invalid rule.
type FieldError struct {
	Index   int
	Message string
}

// Validate compiles every rule of a list and reports the invalid ones with the
// compiler's message (which describes the expression, not user data).
func (e *Evaluator) Validate(rules []store.AccessRule) []FieldError {
	var out []FieldError
	for i, r := range rules {
		if err := e.Check(r.Rule); err != nil {
			out = append(out, FieldError{Index: i, Message: fmt.Sprint(err)})
		}
	}
	return out
}
