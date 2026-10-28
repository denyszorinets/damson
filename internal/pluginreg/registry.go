package pluginreg

import (
	"errors"
	"fmt"
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

// agentRec is an agent with its manifests checksummed once, at SetAgent.
type agentRec struct {
	id, pool string
	caps     map[string]struct{}
	sums     map[pluginKey]string // name+version -> checksum
	mans     map[pluginKey]pluginsdk.Manifest
}

// builtinRec is a manifest the controller links itself.
type builtinRec struct {
	man pluginsdk.Manifest
	sum string
}

// Registry is safe for concurrent use. The zero value is not usable; call New.
type Registry struct {
	mu       sync.RWMutex
	builtins map[pluginKey]builtinRec
	agents   map[string]*agentRec

	vmu        sync.Mutex
	validators map[string]*compiledSchema // by checksum
}

// New builds a registry that knows the given built-in manifests even when no
// agent is connected. Every manifest must be valid.
func New(builtins []pluginsdk.Manifest) (*Registry, error) {
	r := &Registry{
		builtins:   make(map[pluginKey]builtinRec, len(builtins)),
		agents:     make(map[string]*agentRec),
		validators: make(map[string]*compiledSchema),
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
	}
	return r, nil
}

func checked(m pluginsdk.Manifest) (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	return m.Checksum()
}

// SetAgent installs (or replaces) the manifests of a connected agent. A
// manifest that is invalid, or repeats a name+version already seen from the
// same agent, is skipped; the skips are returned (joined) so the caller can
// log them, and the rest are still installed.
func (r *Registry) SetAgent(a AgentInfo) error {
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
	}
	r.mu.Lock()
	r.agents[a.ID] = rec
	r.mu.Unlock()
	return errors.Join(errs...)
}

// RemoveAgent forgets an agent's manifests. Unknown ids are ignored.
func (r *Registry) RemoveAgent(id string) {
	r.mu.Lock()
	delete(r.agents, id)
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
