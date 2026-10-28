package pluginreg

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Provider names an agent that reports a plugin.
type Provider struct {
	AgentID string `json:"agent_id"`
	Pool    string `json:"pool"`
}

// AgentInfo is what the registry keeps of one agent.hello.
type AgentInfo struct {
	ID           string
	Pool         string   // pool name
	Capabilities []string // for example "bin:git"
	Manifests    []pluginsdk.Manifest
}

type pluginKey struct{ name, version string }

// agentRec is an agent with its manifests checksummed once, in Prepare.
type agentRec struct {
	id, pool string
	caps     map[string]struct{}
	sums     map[pluginKey]string // name+version -> checksum
	mans     map[pluginKey]pluginsdk.Manifest
}

// Prepared is an agent record built by Prepare, ready for a cheap Commit.
type Prepared struct{ rec *agentRec }

// builtinRec is a manifest the controller links itself.
type builtinRec struct {
	man pluginsdk.Manifest
	sum string
}

// Registry is safe for concurrent use. The zero value is not usable; call New.
type Registry struct {
	log         *slog.Logger
	builtins    map[pluginKey]builtinRec // immutable after New
	builtinTop  map[string]string        // builtin name -> highest builtin version
	mu          sync.RWMutex
	agents      map[string]*agentRec
	cache       map[string]*Plugin // derived view; nil when stale
	vmu         sync.Mutex
	validators  map[string]*schemaEntry // by checksum, bounded
	maxEntryCap int
}

// New builds a registry that knows the given built-in manifests even when no
// agent is connected. Every manifest must be valid. log receives warnings
// about agents shadowing built-ins (nil uses slog.Default).
func New(builtins []pluginsdk.Manifest, log *slog.Logger) (*Registry, error) {
	if log == nil {
		log = slog.Default()
	}
	r := &Registry{
		log:         log,
		builtins:    make(map[pluginKey]builtinRec, len(builtins)),
		builtinTop:  make(map[string]string),
		agents:      make(map[string]*agentRec),
		validators:  make(map[string]*schemaEntry),
		maxEntryCap: maxCompiledSchemas,
	}
	for _, m := range builtins {
		sum, err := checked(m)
		if err != nil {
			return nil, fmt.Errorf("built-in plugin: %w", err)
		}
		k := pluginKey{m.Name, m.Version}
		if _, dup := r.builtins[k]; dup {
			return nil, fmt.Errorf("built-in plugin %s@%s listed twice", m.Name, m.Version)
		}
		r.builtins[k] = builtinRec{man: m, sum: sum}
		if top, ok := r.builtinTop[m.Name]; !ok || compareSemver(m.Version, top) > 0 {
			r.builtinTop[m.Name] = m.Version
		}
	}
	return r, nil
}

func checked(m pluginsdk.Manifest) (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	return m.Checksum()
}

// Prepare validates and checksums an agent's manifests without touching the
// registry, so the work can run outside any lock. A manifest that is invalid,
// has an oversized or uncompilable input schema, or repeats a name+version
// already seen from the same agent is skipped; the skips are returned (joined)
// for logging and the rest stay in the result. Prepared is never nil.
func (r *Registry) Prepare(a AgentInfo) (*Prepared, error) {
	rec := &agentRec{
		id: a.ID, pool: a.Pool,
		caps: make(map[string]struct{}, len(a.Capabilities)),
		sums: make(map[pluginKey]string, len(a.Manifests)),
		mans: make(map[pluginKey]pluginsdk.Manifest, len(a.Manifests)),
	}
	for _, c := range a.Capabilities {
		rec.caps[c] = struct{}{}
	}
	var errs []error
	for _, m := range a.Manifests {
		sum, err := checked(m)
		if err == nil {
			_, err = r.schemaFor(m, sum) // bounds size and depth, proves it compiles
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("manifest %q from agent %s skipped: %w", m.Name, a.ID, err))
			continue
		}
		k := pluginKey{m.Name, m.Version}
		if _, dup := rec.sums[k]; dup {
			errs = append(errs, fmt.Errorf("manifest %s@%s from agent %s repeated; first kept", m.Name, m.Version, a.ID))
			continue
		}
		rec.sums[k], rec.mans[k] = sum, m
		r.warnShadow(a, m, sum)
	}
	return &Prepared{rec: rec}, errors.Join(errs...)
}

// warnShadow logs an agent manifest that differs from, or outranks, a
// built-in of the same name. Only names, versions and checksums are logged.
func (r *Registry) warnShadow(a AgentInfo, m pluginsdk.Manifest, sum string) {
	top, hasBuiltin := r.builtinTop[m.Name]
	if !hasBuiltin {
		return
	}
	switch b, same := r.builtins[pluginKey{m.Name, m.Version}]; {
	case same && b.sum != sum:
		r.log.Warn("agent reports a manifest that differs from the built-in plugin; it is refused dispatch",
			"agent_id", a.ID, "plugin", m.Name, "version", m.Version, "checksum", sum, "builtin_checksum", b.sum)
	case !same && compareSemver(m.Version, top) > 0:
		r.log.Warn("agent reports a higher version of a built-in plugin; only an explicit major pin resolves to it",
			"agent_id", a.ID, "plugin", m.Name, "version", m.Version, "builtin_version", top)
	}
}

// Commit installs (or replaces) a prepared agent. It is cheap and meant to
// run under the caller's ordering lock.
func (r *Registry) Commit(p *Prepared) {
	r.mu.Lock()
	r.agents[p.rec.id] = p.rec
	r.cache = nil
	r.mu.Unlock()
}

// SetAgent is Prepare followed by Commit.
func (r *Registry) SetAgent(a AgentInfo) error {
	p, err := r.Prepare(a)
	r.Commit(p)
	return err
}

// RemoveAgent forgets an agent's manifests. Unknown ids are ignored.
func (r *Registry) RemoveAgent(id string) {
	r.mu.Lock()
	delete(r.agents, id)
	r.cache = nil
	r.mu.Unlock()
}

// Agents returns the ids of the agents currently contributing, sorted.
func (r *Registry) Agents() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
