package inventory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/store"
)

// scopeTTL is how long a project's chain of scopes is reused. A scope edited on
// another controller applies here within this time.
const scopeTTL = 10 * time.Second

// maxScopeCache bounds the cached chains; it is cleared when full.
const maxScopeCache = 4096

// ChainReader reads a project and its ancestors, root first.
type ChainReader interface {
	ListProjectChain(ctx context.Context, path string) ([]store.Project, error)
}

// ProjectScope is the Scope of the project tree (ARCHITECTURE 12.1): a node is
// allowed when the node_scope of the project and of every ancestor that has one
// is true for it. A project with no scope anywhere allows every node. A scope
// that does not compile or does not evaluate allows nothing (fail closed).
type ProjectScope struct {
	chains ChainReader
	env    *expr.Env
	now    func() time.Time
	log    *slog.Logger

	mu    sync.Mutex
	cache map[string]cachedChain
}

// cachedChain is the compiled scopes of a project's path. broken is set when a
// scope in it does not compile: such a chain allows no node, and the problem is
// logged once per cache fill rather than once per node.
type cachedChain struct {
	programs []*expr.Program
	broken   bool
	exp      time.Time
}

// NewProjectScope returns a Scope over the project tree. A nil clock uses
// time.Now; a nil logger the default.
func NewProjectScope(chains ChainReader, now func() time.Time, log *slog.Logger) (*ProjectScope, error) {
	env, err := expr.NewNodeScopeEnv(expr.Limits{})
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &ProjectScope{chains: chains, env: env, now: now, log: log,
		cache: map[string]cachedChain{}}, nil
}

// chain returns the compiled scopes on the path of project, root first, from
// the cache or the store.
func (s *ProjectScope) chain(ctx context.Context, project string) (cachedChain, error) {
	now := s.now()
	s.mu.Lock()
	c, ok := s.cache[project]
	s.mu.Unlock()
	if ok && now.Before(c.exp) {
		return c, nil
	}
	rows, err := s.chains.ListProjectChain(ctx, project)
	if err != nil {
		return cachedChain{}, fmt.Errorf("reading the project scope: %w", err)
	}
	c = cachedChain{exp: now.Add(scopeTTL)}
	for _, p := range rows {
		if p.NodeScope == "" {
			continue
		}
		prog, err := s.env.CompileRule(p.NodeScope)
		if err != nil {
			s.log.Warn("a project node scope does not compile; its project allows no node", "project", p.Path)
			c.broken = true
			continue
		}
		c.programs = append(c.programs, prog)
	}
	s.mu.Lock()
	if len(s.cache) >= maxScopeCache {
		clear(s.cache)
	}
	s.cache[project] = c
	s.mu.Unlock()
	return c, nil
}

// Allows implements Scope.
func (s *ProjectScope) Allows(ctx context.Context, project string, n store.Node) (bool, error) {
	c, err := s.chain(ctx, project)
	if err != nil {
		return false, err
	}
	if c.broken {
		return false, nil
	}
	if len(c.programs) == 0 {
		return true, nil
	}
	m := TargetMap(n)
	for _, prog := range c.programs {
		ok, err := s.env.EvalNodeScope(prog, m)
		if err != nil {
			// The error text can quote node values: log only what kind it is.
			s.log.Debug("a project node scope did not evaluate; it allows no node", "project", project, "cost_limit", errors.Is(err, expr.ErrCost))
			return false, nil
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}
